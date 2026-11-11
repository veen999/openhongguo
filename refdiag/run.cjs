// Diagnostic A/B harness: runs the reference implementation's own
// playback flow end to end and reports whether a stream came back.
const { fetchAppPlayUrl } = require('./app-source.js');
(async () => {
  const vid = process.argv[2] || '7699003626647194649';
  try {
    const out = await fetchAppPlayUrl(vid, {});
    console.log('REF SUCCESS:', JSON.stringify(out).slice(0, 500));
  } catch (e) {
    console.log('REF FAILED code=', e.code, 'message=', e.message);
  }
})();
