// Opt in with MAESTRO_TEST_CLASSPATH="$HOME/.maestro/lib/*" to execute the
// shipped script in Maestro's real engines, without starting a device.
import * as fs from 'node:fs/promises';
import * as http from 'node:http';
import * as os from 'node:os';
import * as path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const execute = promisify(execFile);
const classpath = process.env.MAESTRO_TEST_CLASSPATH;
const scriptPath = fileURLToPath(new URL('../bin/retrace-maestro.js', import.meta.url));
const harness = `
import java.nio.file.*;
import java.util.*;
import maestro.js.*;
class MaestroScriptTest {
  public static void main(String[] args) throws Exception {
    String script = Files.readString(Path.of(args[0]));
    try (JsEngine engine = args[1].equals("rhino") ? new RhinoJsEngine() : new GraalJsEngine()) {
      for (String command : new String[]{"group checkout", "group --end"}) {
        engine.evaluateScript(script, Map.of("ARGS", command, "RETRACE_MARKER_URL", args[2], "RETRACE_STRICT", "1"), args[0], false);
      }
    }
  }
}`;

describe.skipIf(!classpath)('Maestro installed JavaScript engines', () => {
  it.each(['rhino', 'graaljs'])('%s records markers and surfaces HTTP rejection', async (engine) => {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'retrace-maestro-engine-'));
    const harnessPath = path.join(dir, 'MaestroScriptTest.java');
    const hits: { method?: string; url?: string; contentType?: string; body: string }[] = [];
    let status = 204;
    const server = http.createServer((req, res) => {
      let body = '';
      req.on('data', (chunk) => { body += chunk; });
      req.on('end', () => {
        hits.push({ method: req.method, url: req.url, contentType: req.headers['content-type'], body });
        res.writeHead(status);
        res.end(status === 204 ? '' : 'marker rejected');
      });
    });
    try {
      await fs.writeFile(harnessPath, harness);
      await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
      const addr = server.address();
      if (!addr || typeof addr === 'string') throw new Error('unexpected marker address');
      const args = ['-Dpolyglot.engine.WarnInterpreterOnly=false', '-classpath', classpath!, harnessPath, scriptPath, engine, `http://127.0.0.1:${addr.port}`];
      await execute('java', args, { timeout: 20_000 });
      expect(hits).toEqual([
        { method: 'POST', url: '/group', contentType: 'application/json', body: '{"name":"checkout"}' },
        { method: 'POST', url: '/group/end', contentType: 'application/json', body: '{}' },
      ]);

      status = 400;
      await expect(execute('java', args, { timeout: 20_000 })).rejects.toMatchObject({ stderr: expect.stringMatching(/400: marker rejected/) });
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
      await fs.rm(dir, { recursive: true, force: true });
    }
  }, 45_000);
});
