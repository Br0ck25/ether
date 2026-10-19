package invites

import (
	"html/template"
	"net/http"
)

var landingTemplate = template.Must(template.New("invite").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Join Ether</title><style nonce="{{.Nonce}}">:root{color-scheme:dark;--surface:Canvas;--fg:CanvasText;--muted:GrayText;--primary:LinkText;--border:GrayText}*{box-sizing:border-box}body{margin:0;background:var(--surface);color:var(--fg);font:16px system-ui;display:grid;min-height:100dvh;place-items:center}main{width:min(92vw,30rem);padding:2rem;border:1px solid var(--border);border-radius:1rem}p{color:var(--muted);line-height:1.6}button,a{display:inline-block;background:var(--primary);color:var(--surface);border:0;padding:.8rem 1.2rem;border-radius:.5rem;font:inherit;text-decoration:none;cursor:pointer}small{display:block;margin-top:2rem;color:var(--muted);line-height:1.5}</style></head><body><main><p>ETHER</p><h1>You're invited</h1><p>{{.Label}}</p><p>Sign in to your Plex account to join this server. Your viewing history and recommendations belong to your profile.</p><button id="start">Sign in with Plex</button><a id="approve" hidden target="_blank" rel="noopener noreferrer">Continue to Plex</a><p role="status" id="status"></p><small>Remote playback and other Plex app features follow your real Plex account entitlements. Ether does not provide Plex Pass.</small></main><script nonce="{{.Nonce}}">let handle='',timer;const status=document.getElementById('status'),start=document.getElementById('start'),approve=document.getElementById('approve');async function poll(){try{const r=await fetch(location.pathname+'/poll',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({handle})});if(r.status===409||r.status===502){status.textContent='Your invitation is still being completed. Retrying…';timer=setTimeout(poll,5000);return}if(!r.ok)throw Error(await r.text());const d=await r.json();if(d.done){status.textContent='You have joined. Open Plex and accept the library invitation if prompted.';approve.hidden=true;return}timer=setTimeout(poll,2500)}catch(e){status.textContent=e.message;start.disabled=false}}start.onclick=async()=>{start.disabled=true;status.textContent='Connecting to Plex…';try{const r=await fetch(location.pathname+'/pin',{method:'POST'});if(!r.ok)throw Error(await r.text());const d=await r.json();handle=d.handle;approve.href=d.authUrl;approve.hidden=false;status.textContent='Continue to Plex to approve sign in. Code: '+d.code;poll()}catch(e){status.textContent=e.message;start.disabled=false}};window.addEventListener('pagehide',()=>clearTimeout(timer));</script></body></html>`))

func (s *Service) landing(w http.ResponseWriter, r *http.Request) {
	in, err := s.lookup(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	nonce, err := secret()
	if err != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'nonce-"+nonce+"'; script-src 'nonce-"+nonce+"'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = landingTemplate.Execute(w, struct{ Nonce, Label string }{nonce, in.Label})
}
