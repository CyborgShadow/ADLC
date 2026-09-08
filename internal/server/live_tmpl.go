package server

// The markup, styling and the one script on this dashboard.
//
// See live.go for why the no-JavaScript rule bends here and how it is kept from
// breaking: the script only ever replaces a "working…" placeholder with the
// output of the run that placeholder is about, and it cancels the meta refresh
// only once a stream is genuinely open.

const liveCSS = `
.live{font-size:13px}
.livewait{color:var(--dim);font-style:italic}
.livehint{opacity:.75}
/* A fixed box. It is sized before anything arrives and does not grow with the
   output, because a panel that resizes on every line moves the page under
   whoever is reading it. */
.livetext{margin:8px 0 0;padding:9px 11px;background:#0d1214;border:1px solid var(--line);
border-left:2px solid var(--live);border-radius:4px;height:180px;overflow-y:auto;
white-space:pre-wrap;word-break:break-word;font:12px/1.5 ui-monospace,Consolas,monospace;
color:var(--dim);scrollbar-width:thin}
.livetext.on{color:var(--ink)}
.liveready{display:block;margin-top:8px;font-size:13px}
`

// liveHTML is one running turn. It is its own template because the same
// fragment is used by the Home page, the console page and the dock, and those
// carry three different row types that agree on exactly these two fields.
const liveHTML = `
{{define "live"}}<div class="live" data-live="{{.TurnID}}">
  <div class="livewait">Working — {{.Waited}} so far. A turn is a full agent run, and you are
    watching it as it happens.</div>
  <pre class="livetext">connecting…</pre>
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
  // A page that reloads itself throws away whatever was half-written in its
  // text box, and the console — on Home, on its own page, and in the panel that
  // rides on every other page — is mostly a text box. Home and the console page
  // stop refreshing on the server side when nothing is running; this covers the
  // rest.
  //
  // "Somebody is typing" is read from the DOM at the moment it matters, not
  // remembered from an event. A browser autofilling a name field fires the same
  // input event a person does, and a flag set that way had the page announcing
  // that a turn had finished because you were part-way through typing, to
  // somebody who had typed nothing at all. An unsent question is a textarea
  // with something in it; nothing else counts.
  var watchdog;
  function typing(){
    var t = document.getElementsByTagName("textarea");
    for (var i = 0; i < t.length; i++) if (t[i].value.trim()) return true;
    return false;
  }
  function stopRefresh(){
    var m = document.querySelector("meta[http-equiv=refresh]");
    if (m && m.parentNode) m.parentNode.removeChild(m);
  }
  function reload(){ if (!typing()) location.reload(); }
  document.addEventListener("input", function(ev){
    var t = ev.target;
    if (t && t.tagName === "TEXTAREA" && t.value) stopRefresh();
  }, true);

  var nodes = document.querySelectorAll("[data-live]");
  if (!nodes.length || !window.EventSource) return;

  // Cancelled on OPEN, not on the first byte. An agent can spend ten seconds
  // starting up before it says anything, and a two-second meta refresh running
  // through that window reloads the whole page five times in front of somebody
  // trying to read it. A connection that opened is proof the fallback is not
  // needed; waiting for output is proof of nothing except that the agent is
  // still thinking.
  function takeOver(){
    stopRefresh();
    clearTimeout(watchdog);
    // If the stream then goes quiet for a minute, fall back to a reload rather
    // than sitting on a page that has stopped being told anything.
    watchdog = setTimeout(reload, 60000);
  }
  Array.prototype.forEach.call(nodes, function(node){
    var id = node.getAttribute("data-live");
    var out = node.querySelector(".livetext");
    var started = false;
    var es;
    try { es = new EventSource("/console/live?turn=" + encodeURIComponent(id)); }
    catch (e) { return; }
    es.onopen = takeOver;
    es.addEventListener("text", function(ev){
      var t;
      try { t = JSON.parse(ev.data); } catch (e) { return; }
      if (!t || !out) return;
      takeOver();
      if (!started) { out.textContent = ""; out.className = "livetext on"; started = true; }
      // Follow the output only while already at the bottom. Yanking the box
      // back down while somebody is reading further up is the same rudeness as
      // reloading the page under them.
      var atEnd = out.scrollHeight - out.scrollTop - out.clientHeight < 24;
      out.textContent += t;
      if (atEnd) out.scrollTop = out.scrollHeight;
    });
    es.addEventListener("done", function(){
      es.close();
      clearTimeout(watchdog);
      // Reload into the record: the reply, what it did and what it cost are all
      // read from the ledger, and nothing on screen came from this script. The
      // exception is somebody mid-sentence — losing what they typed to show
      // them an answer they can reach with one click is a bad trade.
      if (typing()) {
        var w = node.querySelector(".livewait");
        if (w) w.textContent = "The turn finished.";
        var a = node.querySelector(".liveready");
        if (a) a.hidden = false;
        return;
      }
      location.reload();
    });
    es.onerror = function(){
      if (es.readyState === 2) setTimeout(reload, 5000);
    };
  });
})();
</script>
`

// tileCSS marks a stage an agent is inside right now.
const tileCSS = `
.livetile{border-left:3px solid var(--live)}
.tilenote{display:block;margin:-2px 0 16px;color:var(--dim);font-size:12.5px;line-height:1.5}
`
