import ghidra.app.script.GhidraScript;
import ghidra.app.decompiler.DecompInterface;
import ghidra.app.decompiler.DecompileResults;
import ghidra.program.model.listing.Function;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;

// PseudoCExporter 是 pogo-malagent-provider 固定的 analyzeHeadless
// -postScript（随运行时包安装在 /runtime/ghidra-scripts/，非模型可控）。
// 参数：<pseudo.c 输出绝对路径> <manifest.json 输出绝对路径>。
// 成功时在 stdout 打印一行机器可校验的标记：
//   MALAGENT_EXPORT_OK <与 manifest.json 相同的 JSON>
// Go 侧据此判定导出真实完成——不能只信进程退出码。
public class PseudoCExporter extends GhidraScript {

    @Override
    public void run() throws Exception {
        String[] args = getScriptArgs();
        if (args.length < 2 || args[0].isEmpty() || args[1].isEmpty()) {
            printerr("usage: PseudoCExporter.java <out.c> <manifest.json>");
            return;
        }
        String outC = args[0];
        String manifestPath = args[1];

        DecompInterface ifc = new DecompInterface();
        ifc.openProgram(currentProgram);
        StringBuilder sb = new StringBuilder();
        int total = 0;
        int exported = 0;
        int failed = 0;
        try {
            for (Function f : currentProgram.getFunctionManager().getFunctions(true)) {
                if (f.isExternal() || f.isThunk()) {
                    continue;
                }
                total++;
                if (monitor.isCancelled()) {
                    throw new IllegalStateException("analysis or decompilation was cancelled");
                }
                DecompileResults r = ifc.decompileFunction(f, 60, monitor);
                if (r.decompileCompleted()) {
                    sb.append("// ==== ").append(f.getName())
                      .append(" @ ").append(f.getEntryPoint()).append(" ====\n");
                    sb.append(r.getDecompiledFunction().getC()).append('\n');
                    exported++;
                } else {
                    failed++;
                    sb.append("// ==== ").append(f.getName())
                      .append(" @ ").append(f.getEntryPoint())
                      .append(" ==== decompile failed: ")
                      .append(r.getErrorMessage()).append('\n');
                }
            }
        } finally {
            ifc.dispose();
        }

        if (monitor.isCancelled()) {
            throw new IllegalStateException("analysis or decompilation was cancelled");
        }
        Files.writeString(Path.of(outC), sb.toString(), StandardCharsets.UTF_8);

        String manifest = "{"
            + "\"tool\":\"ghidra\","
            + "\"program\":\"" + esc(String.valueOf(currentProgram.getName())) + "\","
            + "\"language\":\"" + esc(String.valueOf(currentProgram.getLanguageID())) + "\","
            + "\"executableFormat\":\"" + esc(String.valueOf(currentProgram.getExecutableFormat())) + "\","
            + "\"functionsTotal\":" + total + ","
            + "\"functionsExported\":" + exported + ","
            + "\"functionsFailed\":" + failed
            + "}";
        Files.writeString(Path.of(manifestPath), manifest, StandardCharsets.UTF_8);
        // GhidraScript.println routes through the headless logger, which
        // prefixes/suffixes the line. The native adapter needs a standalone
        // machine marker in this fixed child process's captured stdout.
        System.out.println("MALAGENT_EXPORT_OK " + manifest);
    }

    // esc 做最小的 JSON 字符串转义（程序名/语言 ID 一般只是标识符，但
    // executableFormat 可能带空格与括号，统一走这里）。
    private static String esc(String s) {
        StringBuilder b = new StringBuilder(s.length() + 16);
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"':  b.append("\\\""); break;
                case '\\': b.append("\\\\"); break;
                default:
                    if (c < 0x20) {
                        b.append(String.format("\\u%04x", (int) c));
                    } else {
                        b.append(c);
                    }
            }
        }
        return b.toString();
    }
}
