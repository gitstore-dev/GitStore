// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"

	"github.com/99designs/gqlgen/graphql/playground"
)

//go:embed playground.js
var playgroundScript string

type playgroundBuffer struct {
	bytes.Buffer
	header http.Header
}

func (b *playgroundBuffer) Header() http.Header { return b.header }
func (*playgroundBuffer) WriteHeader(int)       {}

var playgroundShell = template.Must(template.New("endpoint-playground").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>GitStore · API explorer</title>
<style>body{margin:0;background:#f5f6f2;color:#20332c;font:14px ui-monospace,SFMono-Regular,monospace}header{display:flex;align-items:center;gap:16px;padding:14px 20px;border-bottom:1px solid #cdd6ca}strong{letter-spacing:-.04em;font-size:20px;margin-right:16px}form{display:flex;align-items:center;gap:8px;flex:1}input{min-width:180px;flex:1}input,select,button{font:inherit;padding:9px;border:1px solid #a8b7a4;border-radius:4px;background:white;color:inherit}button{background:#24483a;color:white;cursor:pointer}#notice{padding:8px 20px;margin:0;font-size:12px;border-bottom:1px solid #dce1d8}#editor{height:calc(100vh - 108px)}iframe{border:0;width:100%;height:100%;background:white}@media(max-width:680px){header{align-items:flex-start;flex-direction:column;gap:8px}form{width:100%;flex-wrap:wrap}input{min-width:120px}#editor{height:calc(100vh - 155px)}}</style></head>
<body data-kind="{{.Kind}}" data-base="{{.Base}}"><header><strong>GitStore<span style="color:#65806e"> / API</span></strong><form id="endpoint-form"><label for="endpoint-kind">Endpoint</label><select id="endpoint-kind"><option value="admin">Admin</option><option value="storefront">Storefront</option></select><input id="endpoint-url" type="url" aria-label="GraphQL endpoint URL" placeholder="Enter browser-accessible endpoint URL" required><button type="submit">Connect</button></form></header><p id="notice" role="status">Credentials stay with the current editor. Switching clears headers and closes subscriptions.</p><main id="editor"></main><script>{{.Script}}</script></body></html>`))

func endpointPlayground(kind string) http.Handler {
	page := &playgroundBuffer{header: make(http.Header)}
	playground.Handler("GitStore "+kind, "/"+kind+"/graphql").ServeHTTP(page, &http.Request{})
	data := struct {
		Kind, Base string
		Script     template.JS
	}{kind, base64.StdEncoding.EncodeToString(page.Bytes()), template.JS(playgroundScript)} // trusted embedded code only
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := playgroundShell.Execute(w, data); err != nil {
			return
		}
	})
}
