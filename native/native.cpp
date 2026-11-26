// udisk_native: C++ 原生加速模块。
// 目前做一件事：把一批「绝对偏移 + 长度」按多线程直接拷进目标文件，
// 给 Go 端的大文件导出/挂载复制用。Go 负责解析 APFS（决定该读哪些字节），
// C++ 只负责高吞吐搬运，避免逐 extent 走 Go 的小块缓存。
//
// 用法： udisk_native.exe copy <jobfile>
// job 文件格式（纯文本，一行一条）：
//   src=<设备或镜像路径>
//   dst=<目标文件路径>
//   total=<预期总字节，校验用>
//   ext <源偏移> <长度> <目标内偏移>
//   ext <源偏移> <长度> <目标内偏移>
// 输出：一行 JSON {"ok":true,"bytes":N} 或 {"ok":false,"error":"..."}

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>
#include <atomic>
#include <thread>

#ifdef _WIN32
#include <windows.h>
#include <io.h>
#else
#include <unistd.h>
#include <fcntl.h>
#include <sys/stat.h>
#endif

struct Ext { long long off; long long len; long long dst; };

static std::string g_src, g_dst;
static long long g_total = -1;
static std::vector<Ext> g_ext;

static std::string rd_err(const char *what, long long off) {
    char buf[512];
#ifdef _WIN32
    DWORD e = GetLastError();
    sprintf(buf, "%s 失败（偏移 %lld，错误码 %lu）", what, off, (unsigned long)e);
#else
    sprintf(buf, "%s 失败（偏移 %lld，errno %d）", what, off, errno);
#endif
    return std::string(buf);
}

#ifdef _WIN32
typedef HANDLE Handle;
static Handle open_src(const std::string &p) {
    std::wstring w;
    size_t n = p.size();
    w.resize(n);
    for (size_t i = 0; i < n; i++) w[i] = (wchar_t)(unsigned char)p[i]; // UTF-8 -> 直接 ASCII 路径
    // 用 ANSI API 更稳：APFS 设备路径 \\.\PhysicalDriveN 全是 ASCII，
    // 但中文目录需要 UTF-8 -> UTF-16，这里走 MultiByteToWideChar。
    w.clear();
    int wn = MultiByteToWideChar(CP_UTF8, 0, p.c_str(), (int)n, NULL, 0);
    w.resize(wn);
    MultiByteToWideChar(CP_UTF8, 0, p.c_str(), (int)n, &w[0], wn);
    return CreateFileW(w.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL,
                       OPEN_EXISTING, FILE_FLAG_SEQUENTIAL_SCAN, NULL);
}
static int read_at(Handle h, void *buf, long long len, long long off) {
    LARGE_INTEGER li; li.QuadPart = off;
    if (!SetFilePointerEx(h, li, NULL, FILE_BEGIN)) return -1;
    DWORD done = 0;
    char *p = (char *)buf;
    while (done < (DWORD)len) {
        DWORD got = 0;
        if (!ReadFile(h, p + done, (DWORD)(len - done), &got, NULL) || got == 0) return -1;
        done += got;
    }
    return 0;
}
static void close_handle(Handle h) { if (h && h != INVALID_HANDLE_VALUE) CloseHandle(h); }
static int open_dst(Handle *ph, const std::string &p) {
    int wn = MultiByteToWideChar(CP_UTF8, 0, p.c_str(), (int)p.size(), NULL, 0);
    std::wstring w(wn, 0);
    MultiByteToWideChar(CP_UTF8, 0, p.c_str(), (int)p.size(), &w[0], wn);
    *ph = CreateFileW(w.c_str(), GENERIC_WRITE | GENERIC_READ, 0, NULL, CREATE_ALWAYS,
                      FILE_ATTRIBUTE_NORMAL, NULL);
    return (*ph == INVALID_HANDLE_VALUE) ? -1 : 0;
}
static int write_at(Handle h, const void *buf, long long len, long long off) {
    LARGE_INTEGER li; li.QuadPart = off;
    if (!SetFilePointerEx(h, li, NULL, FILE_BEGIN)) return -1;
    DWORD done = 0;
    const char *p = (const char *)buf;
    while (done < (DWORD)len) {
        DWORD put = 0;
        if (!WriteFile(h, p + done, (DWORD)(len - done), &put, NULL) || put == 0) return -1;
        done += put;
    }
    return 0;
}
#else
typedef int Handle;
static Handle open_src(const std::string &p) { return ::open(p.c_str(), O_RDONLY); }
static int read_at(Handle h, void *buf, long long len, long long off) {
    char *p = (char *)buf;
    long long done = 0;
    while (done < len) {
        ssize_t n = pread(h, p + done, (size_t)(len - done), off + done);
        if (n <= 0) return -1;
        done += n;
    }
    return 0;
}
static void close_handle(Handle h) { if (h >= 0) ::close(h); }
static int open_dst(Handle *ph, const std::string &p) {
    *ph = ::open(p.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0644);
    return (*ph < 0) ? -1 : 0;
}
static int write_at(Handle h, const void *buf, long long len, long long off) {
    const char *p = (const char *)buf;
    long long done = 0;
    while (done < len) {
        ssize_t n = pwrite(h, p + done, (size_t)(len - done), off + done);
        if (n <= 0) return -1;
        done += n;
    }
    return 0;
}
#endif

static void out_json(bool ok, long long bytes, const std::string &err) {
    if (ok) {
        printf("{\"ok\":true,\"bytes\":%lld}\n", bytes);
    } else {
        std::string e = err;
        std::string esc;
        for (size_t i = 0; i < e.size(); i++) {
            char c = e[i];
            if (c == '"' || c == '\\') { esc += '\\'; esc += c; }
            else if (c == '\n') esc += "\\n";
            else esc += c;
        }
        printf("{\"ok\":false,\"error\":\"%s\"}\n", esc.c_str());
    }
}

// 线程池：按 extent 下标取任务（原子递增），每个线程 8MB 缓冲。
static std::atomic<long long> g_read_bytes(0);
static std::atomic<int> g_fail(0);
static std::string g_err_msg;

static void worker(Handle src, Handle dst, int idx, size_t nthreads) {
    const size_t BUFSZ = 8u << 20;
    char *buf = (char *)malloc(BUFSZ);
    if (!buf) { g_fail = 1; g_err_msg = "内存不足"; return; }
    for (size_t i = (size_t)idx; i < g_ext.size(); i += nthreads) {
        long long off = g_ext[i].off, left = g_ext[i].len;
        long long dstoff = g_ext[i].dst;
        while (left > 0) {
            long long chunk = left > (long long)BUFSZ ? (long long)BUFSZ : left;
            if (read_at(src, buf, chunk, off) != 0) {
                g_fail = 1; g_err_msg = rd_err("读取源", off); free(buf); return;
            }
            if (write_at(dst, buf, chunk, dstoff) != 0) {
                g_fail = 1; g_err_msg = rd_err("写入目标", dstoff); free(buf); return;
            }
            off += chunk; left -= chunk; dstoff += chunk;
            g_read_bytes += chunk;
        }
    }
    free(buf);
}

static bool load_job(const char *path) {
    FILE *fp = fopen(path, "rb");
    if (!fp) { g_err_msg = "打不开任务文件"; return false; }
    char line[4096];
    while (fgets(line, sizeof(line), fp)) {
        std::string s(line);
        while (!s.empty() && (s.back() == '\n' || s.back() == '\r')) s.pop_back();
        if (s.rfind("src=", 0) == 0) g_src = s.substr(4);
        else if (s.rfind("dst=", 0) == 0) g_dst = s.substr(4);
        else if (s.rfind("total=", 0) == 0) g_total = atoll(s.c_str() + 6);
        else if (s.rfind("ext ", 0) == 0) {
            long long a = 0, b = 0, c = 0;
            if (sscanf(s.c_str() + 4, "%lld %lld %lld", &a, &b, &c) == 3 && b > 0 && a >= 0 && c >= 0) {
                Ext e; e.off = a; e.len = b; e.dst = c; g_ext.push_back(e);
            }
        }
    }
    fclose(fp);
    if (g_src.empty() || g_dst.empty() || g_ext.empty()) { g_err_msg = "任务文件不完整"; return false; }
    return true;
}

int main(int argc, char **argv) {
#ifdef _WIN32
    SetConsoleOutputCP(CP_UTF8);
#endif
    if (argc < 3 || strcmp(argv[1], "copy") != 0) {
        out_json(false, 0, "用法: udisk_native copy <jobfile>");
        return 2;
    }
    if (!load_job(argv[2])) { out_json(false, 0, g_err_msg); return 1; }

    Handle src = open_src(g_src);
#ifdef _WIN32
    if (src == INVALID_HANDLE_VALUE) { out_json(false, 0, rd_err("打开源", 0)); return 1; }
#else
    if (src < 0) { out_json(false, 0, rd_err("打开源", 0)); return 1; }
#endif
    Handle dst;
    if (open_dst(&dst, g_dst) != 0) { close_handle(src); out_json(false, 0, "打不开目标文件"); return 1; }

    static const int NTH = 4;
    std::vector<std::thread> ths;
    for (int i = 0; i < NTH; i++) ths.push_back(std::thread(worker, src, dst, i, (size_t)NTH));
    for (size_t i = 0; i < ths.size(); i++) ths[i].join();

    close_handle(src);
    close_handle(dst);

    if (g_fail) { remove(g_dst.c_str()); out_json(false, 0, g_err_msg); return 1; }
    long long bytes = g_read_bytes;
    if (g_total >= 0 && bytes != g_total) {
        remove(g_dst.c_str());
        char b[256]; sprintf(b, "拷贝字节数 %lld 与预期 %lld 不一致", bytes, g_total);
        out_json(false, 0, b);
        return 1;
    }
    out_json(true, bytes, "");
    return 0;
}
