package server

// The markup, styling and the one script on this dashboard.
//
// See live.go for why the no-JavaScript rule bends here and how it is kept from
// breaking: the script only ever replaces a "working…" placeholder with the
// output of the run that placeholder is about, and it cancels the meta refresh
// only once a stream is actually open.

const liveCSS = `
.live{font-size:13px}
.livewait{color:var(--dim);font-style:italic}
.livehint{opacity:.75}
.livetext{margin:8px 0 0;padding:9px 11px;background:#0d1214;border:1px solid var(--line);
border-left:2px solid var(--live);border-radius:4px;max-height:260px;overflow:auto;
white-space:pre-wrap;word-break:break-word;font:12px/1.5 ui-monospace,Consolas,monospace;
color:var(--dim)}
.livetext.on{color:var(--ink)}
`

// liveHTML is one running turn. It is its own template because the same
// fragment is used by the Home page, the console page and the dock, and those
// carry three different row types that agree on exactly these two fields.
const liveHTML = `
{{define "live"}}<div class="live" data-live="{{.TurnID}}">
  <div class="livewait">Working — {{.Waited}} so far. A turn is a full agent run.
    <span class="livehint">You will see it work as it works; if your browser cannot stream,
    this page refreshes every two seconds instead.</span></div>
  <pre class="livetext" hidden></pre>
</div>{{end}}
`

// liveScript is the whole of the JavaScript on this dashboard.
//
// It does nothing at all unless the page contains a running turn, it never
// renders a result, and every failure mode it has falls back to the reload the
// page would have done anyway.
const liveScript = `<script>
(function(){
  var nodes = document.querySelectorAll("[data-live]");
  if (!nodes.length || !window.EventSource) return;
  var watchdog;
  // The meta refresh is the fallback for a browser that cannot do this. It is
  // cancelled only once bytes have actually arrived, so a stream that never
  // connects leaves the old behaviour in place.
  function takeOver(){
    var m = document.querySelector("meta[http-equiv=refresh]");
    if (m && m.parentNode) m.parentNode.removeChild(m);
    clearTimeout(watchdog);
    // If the stream goes quiet for a minute, fall back to a reload rather than
    // sitting on a page that has stopped being told anything.
    watchdog = setTimeout(function(){ location.reload(); }, 60000);
  }
  Array.prototype.forEach.call(nodes, function(node){
    var id = node.getAttribute("data-live");
    var out = node.querySelector(".livetext");
    var es;
    try { es = new EventSource("/console/live?turn=" + encodeURIComponent(id)); }
    catch (e) { return; }
    es.addEventListener("text", function(ev){
      var t;
      try { t = JSON.parse(ev.data); } catch (e) { return; }
      if (!t || !out) return;
      takeOver();
      out.hidden = false;
      out.className = "livetext on";
      out.textContent += t;
      out.scrollTop = out.scrollHeight;
      var box = node.closest(".scroll") || node.closest(".log");
      if (box) box.scrollTop = box.scrollHeight;
    });
    es.addEventListener("done", function(){
      es.close();
      clearTimeout(watchdog);
      // Reload into the record. The reply, what it did and what it cost are all
      // read from the ledger; nothing on screen came from this script.
      location.reload();
    });
    es.onerror = function(){
      if (es.readyState === 2) setTimeout(function(){ location.reload(); }, 5000);
    };
  });
})();
</script>
`
