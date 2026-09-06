// Maestro runScript entrypoint. Maestro evaluates this in Rhino/GraalJS,
// exposes env values as globals, and supplies http.post; it does not run Node.
// Keep ES5 syntax and no imports so both Maestro engines can execute it.
(function () {
  var args = typeof ARGS === 'undefined' ? '' : String(ARGS);
  var argv = args.trim() ? args.trim().split(/\s+/) : [];
  if (argv[0] !== 'group') {
    throw new Error('retrace-maestro: unknown command ' + JSON.stringify(argv[0] || '') + '; expected "group"');
  }
  var end = argv[1] === '--end';
  var name = argv.slice(1).join(' ');
  if (!end) {
    if (!name) {
      throw new Error('retrace-maestro: "group" requires a name (e.g. `group checkout`), or `--end`');
    }
    // This runtime cannot import retrace-js. Keep the full ValidateComponents
    // guard and strict spellings aligned; script.test.ts checks Node parity.
    if (name.charAt(0) === '.' || /[/\\]/.test(name) || !/^[A-Za-z0-9._-]+$/.test(name)) {
      throw new Error('retrace: invalid group name ' + JSON.stringify(name) +
        ' — must be non-empty, not start with "." and match /^[A-Za-z0-9._-]+$/ (see retrace/runs.ValidateComponents)');
    }
  }

  var rawStrict = typeof RETRACE_STRICT === 'undefined' ? '' : String(RETRACE_STRICT);
  var strictValue = rawStrict.trim().toLowerCase();
  var strict = /^(1|true|yes|on)$/.test(strictValue);
  if (!strict && !/^(0|false|no|off|)$/.test(strictValue)) {
    throw new Error('retrace: RETRACE_STRICT=' + JSON.stringify(rawStrict) +
      ' is not a recognised value. Use one of 1, true, yes, on for strict mode, or ' +
      '0, false, no, off (or unset) for non-strict.');
  }

  var markerUrl = typeof RETRACE_MARKER_URL === 'undefined' ? '' : String(RETRACE_MARKER_URL);
  if (!markerUrl) {
    if (strict) {
      throw new Error('retrace: no active run. This fixture writes checkpoints and flow-part markers into the ' +
        'directory `retrace run` creates, and found neither RETRACE_RUN_DIR nor RETRACE_MARKER_URL ' +
        'in the environment.\n' +
        '  Run your tests through retrace:  retrace run --flow <name> -- <your test command>\n' +
        '  Or unset RETRACE_STRICT to let checkpoints be no-ops outside a run.');
    }
    return;
  }

  var url = markerUrl.replace(/\/+$/, '') + (end ? '/group/end' : '/group');
  var response = http.post(url, {
    headers: { 'content-type': 'application/json' },
    body: end ? '{}' : JSON.stringify({ name: name })
  });
  if (!response.ok) {
    throw new Error('retrace-maestro: marker POST ' + url + ' failed with ' + response.status + ': ' + response.body);
  }
}());
