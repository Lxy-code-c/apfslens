# -*- coding: utf-8 -*-
"""U盘格式化工具 诊断报告生成器（Python 引擎）。

由主程序调用：apfslens_doctor.py <facts.txt>
facts.txt 是 Go 端收集的原始事实（key=value + diag_begin/diag_end + log_begin/log_end），
本脚本只负责排版、归纳和给建议；不联网、不写注册表。

没有 Python 的机器上，主程序会用 Go 内置排版，功能等价。
"""
import sys
import re


def load(path):
    txt = open(path, "r", encoding="utf-8", errors="replace").read()
    kv, diag, log = {}, "", []
    section = None
    for line in txt.splitlines():
        if line == "diag_begin":
            section = "diag"
            diag = ""
            continue
        if line == "diag_end":
            section = None
            continue
        if line == "log_begin":
            section = "log"
            continue
        if line == "log_end":
            section = None
            continue
        if section == "diag":
            diag += line + "\n"
            continue
        if section == "log":
            log.append(line)
            continue
        if "=" in line:
            k, v = line.split("=", 1)
            kv.setdefault(k, []).append(v)
    return kv, diag.strip(), log


def main():
    if len(sys.argv) < 2:
        print("用法：apfslens_doctor.py <facts.txt>")
        return 2
    kv, diag, log = load(sys.argv[1])

    def one(k, d=""):
        v = kv.get(k)
        return v[-1] if v else d

    version = one("version", "?")
    arch = one("arch", "?")
    admin = one("admin", "false") == "true"
    wv2 = one("webview2", "false") == "true"
    disks = kv.get("disk", [])
    natives = kv.get("native", [])

    out = []
    out.append("=== U盘格式化工具 诊断报告（Python 引擎）===")
    out.append("版本 %s    架构 %s    时间 %s" % (version, arch, one("time", "")))
    out.append("")
    out.append("【环境】")
    out.append("  管理员权限：%s" % ("有" if admin else "无"))
    out.append("  WebView2  ：%s" % ("已安装" if wv2 else "缺失（会用浏览器兜底界面）"))
    out.append("  用户目录  ：%s" % one("userdata", ""))
    out.append("")

    out.append("【磁盘】")
    if not disks:
        out.append("  没有扫描到磁盘")
    for d in disks:
        out.append("  " + d)
    out.append("")

    out.append("【模块】")
    for n in natives or ["（没有模块信息）"]:
        out.append("  " + n)
    out.append("")

    # 给建议：把能自动判断的都判断掉
    tips = []
    if not admin:
        tips.append("当前不是管理员：读原始磁盘、挂载盘符都会受限，请用「解除拦截并启动.cmd」右键以管理员运行。")
    if not wv2:
        tips.append("缺 WebView2 运行库：装一次 Microsoft Edge WebView2 Runtime 即可，否则界面会退到浏览器模式。")
    if any("缺失" in n and "apfslens_probe" in n for n in natives):
        tips.append("缺 apfslens_probe.exe：C 引擎的分区交叉核对不可用。它必须和主程序放同一个文件夹。")
    if any("缺失" in n and "apfslens_native" in n for n in natives):
        tips.append("缺 apfslens_native.exe：大文件导出会退回 Go 路径（能跑，只是慢）。")
    if any("缺失" in n and "apfslens_doctor" in n for n in natives):
        tips.append("缺 apfslens_doctor.py：本报告会改为 Go 内置排版。")
    apfs_disks = [d for d in disks if "APFS=True" in d]
    if disks and not apfs_disks:
        tips.append("扫到的盘里没有标记 APFS：确认盘已插好、没被别的程序独占；必要时在「读取 APFS」页点刷新。")
    for d in apfs_disks:
        if "可打开" not in d:
            tips.append("这块盘标记了 APFS 但打不开：" + d.split("  ")[0] + " —— 多半是权限或盘被占用。")
    if diag:
        tips.append("有 apfslens-diag.txt：里面记录了上次扫描失败的具体原因，可连同本报告一起发我。")
    if not log:
        tips.append("没有日志：程序可能还没真正启动过，或日志目录被清理。")

    out.append("【建议】")
    if tips:
        for i, t in enumerate(tips, 1):
            out.append("  %d) %s" % (i, t))
    else:
        out.append("  各项看起来正常。如果仍然有问题，请把本报告发给开发者。")
    out.append("")

    out.append("【APFS 诊断文件】")
    out.append(diag if diag else "  （无）")
    out.append("")
    out.append("【日志尾部】")
    if not log:
        out.append("  （无）")
    else:
        for l in log[-120:]:
            out.append("  " + l)
    print("\n".join(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
