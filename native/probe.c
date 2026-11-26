/* udisk_probe: 独立的 C 引擎，只做一件事——把分区表读出来，找出 APFS 分区。
 * 它不依赖 Go 的解析结果，用于交叉核对「这块盘到底有没有 APFS」，
 * 顺便读出 APFS 容器块大小，避免 Go 端漏判。输出 JSON 一行。
 *
 * 用法： udisk_probe probe <设备或镜像路径> <GPT 起始字节偏移>
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

#ifdef _WIN32
#include <windows.h>
typedef HANDLE FH;
#else
#include <fcntl.h>
#include <unistd.h>
typedef int FH;
#endif

static FH g_f;
static long long g_base;

static int rd(void *buf, long long len, long long off) {
#ifdef _WIN32
    LARGE_INTEGER li; li.QuadPart = off;
    if (!SetFilePointerEx(g_f, li, NULL, FILE_BEGIN)) return -1;
    DWORD got = 0;
    if (!ReadFile(g_f, buf, (DWORD)len, &got, NULL) || got != (DWORD)len) return -1;
    return 0;
#else
    long long done = 0;
    while (done < len) {
        ssize_t n = pread(g_f, (char *)buf + done, (size_t)(len - done), off + done);
        if (n <= 0) return -1;
        done += n;
    }
    return 0;
#endif
}

/* GPT 里 APFS 的类型 GUID 原始字节（前 3 段小端，后 2 段大端）：
 * 7C3457EF-0000-11AA-AA11-00306543ECAC */
static const unsigned char APFS_GUID[16] = {
    0xEF, 0x57, 0x34, 0x7C, 0x00, 0x00, 0xAA, 0x11,
    0xAA, 0x11, 0x00, 0x30, 0x65, 0x43, 0xEC, 0xAC
};

static uint32_t crc32_tab[256];
static void crc32_init(void) {
    for (uint32_t i = 0; i < 256; i++) {
        uint32_t c = i;
        for (int k = 0; k < 8; k++) c = (c & 1) ? (0xEDB88320u ^ (c >> 1)) : (c >> 1);
        crc32_tab[i] = c;
    }
}
static uint32_t crc32(const unsigned char *p, size_t n) {
    uint32_t c = 0xFFFFFFFFu;
    for (size_t i = 0; i < n; i++) c = crc32_tab[(c ^ p[i]) & 0xFF] ^ (c >> 8);
    return c ^ 0xFFFFFFFFu;
}

static uint64_t le64(const unsigned char *p) {
    uint64_t v = 0;
    for (int i = 7; i >= 0; i--) v = (v << 8) | p[i];
    return v;
}
static uint32_t le32(const unsigned char *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

int main(int argc, char **argv) {
#ifdef _WIN32
    SetConsoleOutputCP(CP_UTF8);
#endif
    if (argc < 4 || strcmp(argv[1], "probe") != 0) {
        printf("{\"ok\":false,\"error\":\"usage: udisk_probe probe <path> <offset>\"}\n");
        return 2;
    }
#ifdef _WIN32
    {
        int wn = MultiByteToWideChar(CP_UTF8, 0, argv[2], -1, NULL, 0);
        wchar_t *w = (wchar_t *)malloc(sizeof(wchar_t) * wn);
        MultiByteToWideChar(CP_UTF8, 0, argv[2], -1, w, wn);
        g_f = CreateFileW(w, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL,
                          OPEN_EXISTING, FILE_FLAG_SEQUENTIAL_SCAN, NULL);
        free(w);
        if (g_f == INVALID_HANDLE_VALUE) {
            printf("{\"ok\":false,\"error\":\"open failed %lu\"}\n", (unsigned long)GetLastError());
            return 1;
        }
    }
#else
    g_f = open(argv[2], O_RDONLY);
    if (g_f < 0) {
        printf("{\"ok\":false,\"error\":\"open failed\"}\n");
        return 1;
    }
#endif
    g_base = atoll(argv[3]);
    crc32_init();

    unsigned char hdr[512];
    if (rd(hdr, 512, g_base + 512) != 0) {
        printf("{\"ok\":false,\"error\":\"read gpt header failed\"}\n");
        return 1;
    }
    if (memcmp(hdr, "EFI PART", 8) != 0) {
        /* 没有 GPT：检查 MBR 是否只是保护性分区（0xEE），报告 mbr 即可 */
        unsigned char mbr[512];
        int mbr_ok = (rd(mbr, 512, g_base) == 0);
        printf("{\"ok\":true,\"gpt\":false,\"mbr\":%s,\"apfs\":[]}\n", mbr_ok ? "true" : "false");
        return 0;
    }

    /* 头部 CRC 校验：前 4 字节是 CRC 字段本身，校验时先当 0 */
    uint32_t hsize = le32(hdr + 12);
    if (hsize > 0 && hsize <= 512) {
        unsigned char tmp[512];
        memcpy(tmp, hdr, hsize);
        uint32_t want = le32(tmp + 16);
        memset(tmp + 16, 0, 4);
        uint32_t got = crc32(tmp, hsize);
        if (got != want) {
            printf("{\"ok\":false,\"error\":\"gpt header crc mismatch\"}\n");
            return 1;
        }
    }

    uint64_t ent_lba = le64(hdr + 72);
    uint32_t ent_cnt = le32(hdr + 80);
    uint32_t ent_sz = le32(hdr + 84);
    if (ent_sz < 128 || ent_sz > 1024 || ent_cnt == 0 || ent_cnt > 4096) {
        printf("{\"ok\":false,\"error\":\"gpt entries invalid\"}\n");
        return 1;
    }
    long long total = (long long)ent_cnt * ent_sz;
    unsigned char *ents = (unsigned char *)malloc((size_t)total);
    if (!ents) {
        printf("{\"ok\":false,\"error\":\"oom\"}\n");
        return 1;
    }
    if (rd(ents, total, g_base + (long long)ent_lba * 512) != 0) {
        free(ents);
        printf("{\"ok\":false,\"error\":\"read gpt entries failed\"}\n");
        return 1;
    }
    uint32_t want_crc = le32(hdr + 88);
    int crc_ok = (crc32(ents, (size_t)total) == want_crc);

    printf("{\"ok\":true,\"gpt\":true,\"crc\":%s,\"apfs\":[", crc_ok ? "true" : "false");
    int n = 0;
    for (uint32_t i = 0; i < ent_cnt; i++) {
        unsigned char *e = ents + (size_t)i * ent_sz;
        if (memcmp(e, APFS_GUID, 16) != 0) continue;
        uint64_t first = le64(e + 32), last = le64(e + 40);
        if (last < first) continue;
        long long off = (long long)first * 512;
        long long size = (long long)(last - first + 1) * 512;
        unsigned char nx[512];
        uint32_t bs = 0;
        int has_nx = 0;
        if (rd(nx, 512, g_base + off) == 0 && memcmp(nx + 32, "NXSB", 4) == 0) {
            has_nx = 1;
            bs = le32(nx + 36);
        }
        printf("%s{\"offset\":%lld,\"size\":%lld,\"bs\":%u,\"nxsb\":%s}",
               n ? "," : "", off, size, bs, has_nx ? "true" : "false");
        n++;
    }
    printf("]}\n");
    free(ents);
    return 0;
}
