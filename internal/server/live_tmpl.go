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
.liveready{display:block;margin-top:8px;font-size:13px}
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
  <a class="liveready" href="" hidden>The turn finished — open the answer.
    <span class="livehint">Not loaded automatically because you are part-way through typing.</span></a>
</div>{{end}}
`

// liveScript is the whole of the JavaScript on this dashboard.
//
// It does two things, and both of them are about not making a person repeat
// themselves: it streams a running turn, and it stops the page reloading out
// from under somebody who is typing into it.
//
// It never renders a result, and every failure mode it has falls back to the
// reload the page would have done anyway.
const liveScript = `<script>
(function(){
  // A page that reloads itself every fifteen seconds throws away whatever was
  // half-written in its text box, and the console — on Home, on its own page,
  // and in the panel that rides on every other page — is mostly a text box.
  // Home and the console page stop refreshing on the server side when nothing
  // is running; this covers the rest: the moment anybody types anywhere, the
  // refresh is off until they navigate.
  var dirty = false, watchdog;
  function stopRefresh(){
    var m = document.querySelector("meta[http-equiv=refresh]");
    if (m && m.parentNode) m.parentNode.removeChild(m);
  }
  document.addEventListener("input", function(ev){
    var t = ev.target;
    if (!t || !t.value) return;
    if (t.tagName !== "TEXTAREA" && t.tagName !== "INPUT") return;
    dirty = true;
    clearTimeout(watchdog);
    stopRefresh();
  }, true);
  // Submitting is the end of typing, so the page may look after itself again.
  document.addEventListener("submit", function(){ dirty = false; }, true);

  var nodes = document.querySelectorAll("[data-live]");
  if (!nodes.length || !window.EventSource) return;

  // The meta refresh is also the fallback for a browser that cannot stream, so
  // it is cancelled only once bytes have actually arrived.
  function takeOver(){
    stopRefresh();
    clearTimeout(watchdog);
    // If the stream goes quiet for a minute, fall back to a reload rather than
    // sitting on a page that has stopped being told anything.
    if (!dirty) watchdog = setTimeout(function(){ reload(); }, 60000);
  }
  function reload(){ if (!dirty) location.reload(); }
  function offerReload(node){
    var a = node.querySelector(".liveready");
    if (a) a.hidden = false;
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
      // Reload into the record: the reply, what it did and what it cost are all
      // read from the ledger, and nothing on screen came from this script. The
      // exception is somebody mid-sentence — losing what they typed to show
      // them an answer they can reach with one click is a bad trade.
      if (dirty) { offerReload(node); return; }
      location.reload();
    });
    es.onerror = function(){
      if (es.readyState === 2) setTimeout(reload, 5000);
    };
  });
})();
</script>
`
