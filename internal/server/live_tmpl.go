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
{{define "live"}}<div class="live" data-live="{{.LiveID}}">
  <div class="livewait">Working — {{.Waited}} so far. You are watching it as it happens.</div>
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
  // Two jobs, both about not making a person repeat themselves: stream a
  // running turn, and never reload the page out from under somebody who is
  // part-way through writing in it.
  //
  // "Somebody is typing" is the difference between a field's value NOW and the
  // value it held when this page loaded. Not an event flag, and not merely "the
  // box has text in it": reloading a page makes the browser RESTORE what was in
  // its fields, so a box refilled by the browser looked exactly like a box
  // somebody was typing in — and the page then refused to reload itself ever
  // again, telling the reader it was because they were part-way through typing
  // something they had never typed.
  //
  // Every text field counts, not only textareas. Accepting a recommendation
  // means typing a reason into a one-line input and nothing else, so watching
  // textareas alone left that reason as the one thing a refresh could still
  // destroy — and the reason is the part that is useful in six months.
  var watchdog, fields = [], before = [];
  (function(){
    var all = document.querySelectorAll("textarea, input[type=text], input:not([type])");
    for (var i = 0; i < all.length; i++) { fields.push(all[i]); before.push(all[i].value); }
  })();
  function typing(){
    for (var i = 0; i < fields.length; i++) {
      if (fields[i].value.trim() && fields[i].value !== before[i]) return true;
    }
    return false;
  }
  function stopRefresh(){
    var m = document.querySelector("meta[http-equiv=refresh]");
    if (m && m.parentNode) m.parentNode.removeChild(m);
  }
  function reload(){ if (!typing()) location.reload(); }
  document.addEventListener("input", function(ev){
    if (typing()) stopRefresh();
  }, true);

  var nodes = document.querySelectorAll("[data-live]");
  if (!nodes.length || !window.EventSource) return;

  // The meta refresh is cancelled on OPEN, not on the first byte: an agent can
  // spend ten seconds starting up, and a two-second refresh running through
  // that window reloads the page five times in front of somebody reading it.
  function takeOver(){
    stopRefresh();
    clearTimeout(watchdog);
    // A stream that has said nothing at all for two minutes — not even the
    // server's ping — is a stream nobody is going to hear from. Falling back to
    // a reload beats sitting on a page that has stopped being told anything.
    watchdog = setTimeout(reload, 120000);
  }
  Array.prototype.forEach.call(nodes, function(node){
    var id = node.getAttribute("data-live");
    var out = node.querySelector(".livetext");
    var wait = node.querySelector(".livewait");
    var started = false, since = Date.now();
    var es;
    try { es = new EventSource("/live?id=" + encodeURIComponent(id)); }
    catch (e) { return; }
    // The elapsed time was rendered by the server and frozen at page load, so
    // it went on claiming the same figure for as long as the page stayed up.
    // Once the stream is running, the page owns it.
    var ticking = setInterval(function(){
      if (!wait) return;
      var s = Math.round((Date.now() - since) / 1000);
      var m = Math.floor(s / 60);
      wait.textContent = "Working — " + (m ? m + "m " + (s % 60) + "s" : s + "s") +
        " so far. A turn is a full agent run, and you are watching it as it happens.";
    }, 1000);
    function ended(text){
      clearInterval(ticking);
      clearTimeout(watchdog);
      if (wait) wait.textContent = text;
    }
    es.onopen = takeOver;
    es.addEventListener("ping", takeOver);
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
      ended("The turn finished.");
      // Reload into the record: the reply, what it did and what it cost are all
      // read from the ledger, and nothing on screen came from this script. The
      // exception is somebody mid-sentence — losing what they typed to show
      // them an answer they can reach with one click is a bad trade.
      if (typing()) {
        var a = node.querySelector(".liveready");
        if (a) a.hidden = false;
        return;
      }
      location.reload();
    });
    es.onerror = function(){
      if (es.readyState === 2) { ended("The stream ended."); setTimeout(reload, 5000); }
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

// saidCSS renders an agent's own account so it does not read as a finding.
const saidCSS = `
.said{border-left:3px solid var(--line)}
.said .body{white-space:pre-wrap;word-break:break-word;line-height:1.6}
.said details{margin-top:10px}
.said summary{cursor:pointer;color:var(--accent);font-size:13px}
.said pre{white-space:pre-wrap;word-break:break-word;background:#0d1214;border:1px solid var(--line);
border-radius:4px;padding:10px 12px;font-size:12.5px;line-height:1.55;margin-top:8px;max-height:420px;overflow:auto}
.said ul{margin:6px 0 0;padding-left:20px;font-size:13px}
`

// depsCSS renders a dependency chain as a small work-breakdown tree.
//
// It was a run-on line of item ids. Four levels of chain read as prose is not
// something anybody can hold: what a person wants is the shape — what is
// waiting on what, and which one at the bottom actually has to happen first.
// Closed by default, because most rows are not blocked and a page of open trees
// is its own kind of noise.
const depsCSS = `
.deps{margin-top:5px}
.deps>summary{cursor:pointer;color:var(--dim);font-size:12.5px;list-style:none;
display:inline-flex;gap:6px;align-items:center}
.deps>summary::-webkit-details-marker{display:none}
.deps>summary::before{content:"\25B8";color:var(--accent);font-size:10px;
transition:transform .12s;display:inline-block}
.deps[open]>summary::before{transform:rotate(90deg)}
.deps>summary:hover{color:var(--ink)}
.deptree{margin:7px 0 3px;border-left:1px solid var(--line);padding:2px 0 2px 0}
.dep{display:flex;gap:8px;align-items:baseline;padding:3px 0 3px 10px;position:relative;
font-size:12.5px;line-height:1.4}
.dep::before{content:"";position:absolute;left:0;top:11px;width:8px;height:1px;background:var(--line)}
.dep .id{font-family:ui-monospace,Consolas,monospace;font-size:12px}
.dep .ttl{color:var(--dim);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:46ch}
.dep.first{background:linear-gradient(90deg,rgba(89,169,196,.10),transparent 60%);border-radius:3px}
.dep.first .ttl{color:var(--ink)}
.dep .startshere{color:var(--live);font-size:11px;white-space:nowrap}
`

// depTreeHTML is the pop-out itself, used by every page that shows a blocked
// item so there is one rendering of a dependency rather than three.
const depTreeHTML = `
{{define "deps"}}{{if .Waiting}}<details class="deps">
  <summary>{{.Says}}</summary>
  <div class="deptree">
    {{range .Waiting}}<div class="dep{{if .Root}} first{{end}}" style="margin-left:{{.Indent}}px">
      <span class="pill {{.Class}}">{{.State}}</span>
      <a class="id" href="/item/{{.ID}}">{{.ID}}</a>
      <span class="ttl">{{.Title}}</span>
      {{if .Root}}<span class="startshere">starts here</span>{{end}}
    </div>{{end}}
  </div>
</details>{{end}}{{end}}
`
