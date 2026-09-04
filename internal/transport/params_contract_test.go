package transport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestParamsContractsAgreeWithRegisteredValidators(t *testing.T) {
	server := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	registered := make(map[string]methodSpec)
	for method, spec := range server.methods {
		registered[method] = spec
	}

	entries, entriesErr := os.ReadDir(contractDir)
	if entriesErr != nil {
		t.Fatalf("read contracts dir: %v", entriesErr)
	}
	contracts := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".params.schema.json") {
			continue
		}
		method := strings.TrimSuffix(name, ".params.schema.json")
		contracts[method] = struct{}{}
		if _, ok := registered[method]; !ok {
			t.Errorf("params contract %s has no registered method", name)
		}
	}

	invalid := [][]byte{
		[]byte(`[]`),
		[]byte(`"scalar"`),
		[]byte(`true`),
		[]byte(`{"unknown":true}`),
		[]byte(`{"id":null,"body":null,"title":null,"query":null,"ids":null}`),
		[]byte(`{"id":123}`),
		[]byte(`{"body":123}`),
		[]byte(`{"title":123}`),
		[]byte(`{"query":123}`),
		[]byte(`{"ids":"not-an-array"}`),
	}
	for _, probe := range []struct {
		field string
		size  int
	}{
		{field: "id", size: 129},
		{field: "body", size: 200001},
		{field: "title", size: 201},
		{field: "query", size: 1001},
	} {
		raw, marshalErr := json.Marshal(map[string]string{probe.field: strings.Repeat("x", probe.size)})
		if marshalErr != nil {
			t.Fatalf("marshal %s probe: %v", probe.field, marshalErr)
		}
		invalid = append(invalid, raw)
	}
	idsProbe, idsErr := json.Marshal(map[string][]string{"ids": {strings.Repeat("x", 129)}})
	if idsErr != nil {
		t.Fatalf("marshal ids probe: %v", idsErr)
	}
	invalid = append(invalid, idsProbe)
	valid := map[string][][]byte{
		"notes.create": {
			[]byte(`{}`),
			[]byte(`{"body":"body"}`),
		},
		"notes.delete": {
			[]byte(`{"id":"note-1"}`),
		},
		"notes.get": {
			[]byte(`{"id":"note-1"}`),
		},
		"notes.list": {
			[]byte(`{}`),
		},
		"notes.search": {
			[]byte(`{}`),
			[]byte(`{"query":"term"}`),
		},
		"notes.update": {
			[]byte(`{"id":"note-1"}`),
			[]byte(`{"id":"note-1","body":"updated"}`),
		},
		"snippets.create": {
			[]byte(`{}`),
			[]byte(`{"title":"title","body":"body"}`),
		},
		"snippets.delete": {
			[]byte(`{"id":"snippet-1"}`),
		},
		"snippets.list": {
			[]byte(`{}`),
		},
		"snippets.reorder": {
			[]byte(`{}`),
			[]byte(`{"ids":[]}`),
		},
		"snippets.update": {
			[]byte(`{"id":"snippet-1"}`),
			[]byte(`{"id":"snippet-1","title":"title","body":"body"}`),
		},
		"app.about": {
			[]byte(`{}`),
		},
		"backup.create": {
			[]byte(`{}`),
		},
		"backup.preview": {
			[]byte(`{"contents":"{}","strategy":"merge"}`),
		},
		"backup.restore": {
			[]byte(`{"contents":"{}","strategy":"replace","previewToken":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`),
		},
		"backup.saveToFile": {
			[]byte(`{"fileName":"backup.json","contents":"{}"}`),
		},
		"connections.passwordResolved": {
			[]byte(`{"requestId":"request-1","outcome":"cancelled"}`),
		},
		"connections.test": {
			[]byte(`{"profileId":"ssh:test:1"}`),
		},
		"connections.trustHostKey": {
			[]byte(`{"host":"host.example.com:22","key":"b2ZmZXJlZC1rZXktYmxvYg=="}`),
		},
		"dialog.openDirectory": {
			[]byte(`{}`),
		},
		"dialog.openFile": {
			[]byte(`{}`),
		},
		"dialog.openFileForUpload": {
			[]byte(`{}`),
		},
		"endpoints.create": {
			[]byte(`{"name":"Local","baseUrl":"https://example.com/v1","schema":"openai-compatible","models":[{"name":"model"}]}`),
		},
		"endpoints.delete": {
			[]byte(`{"id":"endpoint:test:1"}`),
		},
		"endpoints.list": {
			[]byte(`{}`),
		},
		"endpoints.probe": {
			[]byte(`{"baseUrl":"https://example.com/v1","model":"model"}`),
		},
		"endpoints.update": {
			[]byte(`{"id":"endpoint:test:1","name":"Local","baseUrl":"https://example.com/v1","schema":"openai-compatible","models":[{"name":"model"}]}`),
		},
		"fs.complete": {
			[]byte(`{"text":"/tmp","cwd":"/"}`),
		},
		"groups.apply": {
			[]byte(`[{"id":"group:test:1","name":"group"}]`),
		},
		"groups.create": {
			[]byte(`{"name":"group"}`),
		},
		"groups.delete": {
			[]byte(`{"id":"group:test:1"}`),
		},
		"groups.impact": {
			[]byte(`{"deleteGroupId":"group:test:1"}`),
		},
		"groups.list": {
			[]byte(`{}`),
		},
		"groups.update": {
			[]byte(`{"id":"group:test:1","name":"group"}`),
		},
		"history.query": {
			[]byte(`{"scope":"pane","paneId":"pane-1"}`),
			[]byte(`{"scope":"everywhere"}`),
			[]byte(`{"scope":"directory","cwd":"/tmp"}`),
			[]byte(`{"scope":"host","host":""}`),
		},
		"history.record": {
			[]byte(`{"command":"echo hi","source":"user","status":"success","paneId":"pane-1"}`),
		},
		"history.status": {
			[]byte(`{}`),
		},
		"ledger.artifact": {
			[]byte(`{"id":"artifact-1"}`),
		},
		"ledger.bind": {
			[]byte(`{"envelope":{"id":"entry-1","sessionId":"session-1","cwd":"/tmp","kind":"shell","sensitivity":"normal","attemptId":"attempt-1"}}`),
		},
		"ledger.capture": {
			[]byte(`{"entryId":"entry-1","artifactId":"0198f2b0-0000-7000-8000-00000000c001","mediaType":"application/vt","captureVersion":1,"seq":1,"body":"body"}`),
		},
		"ledger.close": {
			[]byte(`{"envelope":{"id":"entry-1","sessionId":"session-1","cwd":"/tmp","kind":"shell","sensitivity":"normal"},"status":"success","facts":{"terminationReason":"completed"}}`),
		},
		"ledger.get": {
			[]byte(`{"id":"entry-1"}`),
		},
		"ledger.open": {
			[]byte(`{"envelope":{"id":"entry-1","sessionId":"session-1","cwd":"/tmp","kind":"shell","sensitivity":"normal"}}`),
		},
		"ledger.query": {
			[]byte(`{"scope":"everywhere"}`),
		},
		"lifecycle.establishAck": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","lane":"lane-1","domain":"domain-1","epoch":1,"generation":"generation-1"}`),
		},
		"lifecycle.recoverAck": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","generation":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`),
		},
		"lifecycle.submitAttempt": {
			[]byte(`{"domain":"domain-1","command":"echo hi","source":"user"}`),
		},
		"notify.bell": {
			[]byte(`{"sessionId":"session-1"}`),
		},
		"notify.catalogue": {
			[]byte(`{}`),
		},
		"notify.feed.markRead": {
			[]byte(`{}`),
		},
		"notify.feed.read": {
			[]byte(`{}`),
		},
		"notify.paneWorkFinished": {
			[]byte(`{"sessionId":"session-1"}`),
		},
		"notify.raise": {
			[]byte(`{"sessionId":"session-1","title":"title","body":"body"}`),
		},
		"policy.get": {
			[]byte(`{}`),
		},
		"policy.set": {
			[]byte(`{"policy":{}}`),
		},
		"ports.pause": {
			[]byte(`{"profileId":"profile-1","paused":false,"visible":true}`),
		},
		"ports.sample": {
			[]byte(`{"profileId":"profile-1"}`),
		},
		"ports.status": {
			[]byte(`{"profileId":"profile-1"}`),
		},
		"ports.visible": {
			[]byte(`{"profileId":"profile-1","paused":false,"visible":true}`),
		},
		"profiles.create": {
			[]byte(`{"name":"profile","type":"ssh","options":{"host":"host.example.com"}}`),
		},
		"profiles.delete": {
			[]byte(`{"id":"profile:test:1"}`),
		},
		"profiles.effective": {
			[]byte(`{}`),
			[]byte(`{"ids":[]}`),
		},
		"profiles.importTabby": {
			[]byte(`{"config":"profiles: []"}`),
		},
		"profiles.list": {
			[]byte(`{}`),
		},
		"profiles.moveImpact": {
			[]byte(`{"profileIds":["profile:test:1"]}`),
		},
		"profiles.patch": {
			[]byte(`{"id":"profile:test:1"}`),
		},
		"profiles.tabbyExecute": {
			[]byte(`{"planToken":"0000000000000000000000000000000000000000000000000000000000000000"}`),
		},
		"profiles.tabbyPreview": {
			[]byte(`{"config":"profiles: []"}`),
		},
		"profiles.update": {
			[]byte(`{"id":"profile:test:1","name":"profile","type":"ssh","options":{"host":"host.example.com"}}`),
		},
		"roles.assign": {
			[]byte(`{"role":"answering"}`),
		},
		"roles.list": {
			[]byte(`{}`),
		},
		"roles.setDefault": {
			[]byte(`{"endpointId":"","model":""}`),
		},
		"session.signal": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","signal":"interrupt"}`),
		},
		"sessions.status": {
			[]byte(`{"profileIds":[]}`),
		},
		"settings.describe": {
			[]byte(`{}`),
		},
		"settings.getSnapshot": {
			[]byte(`{}`),
		},
		"settings.reset": {
			[]byte(`{"key":"clipboard.osc52Suppressed"}`),
		},
		"settings.secretDelete": {
			[]byte(`{"key":"secret.key"}`),
		},
		"settings.secretExists": {
			[]byte(`{"key":"secret.key"}`),
		},
		"settings.secretSet": {
			[]byte(`{"key":"secret.key","value":"secret"}`),
		},
		"settings.set": {
			[]byte(`{"key":"clipboard.osc52Suppressed","value":true}`),
		},
		"skills.audit": {
			[]byte(`{"name":"deploy"}`),
		},
		"skills.file": {
			[]byte(`{"name":"deploy","path":"references/hosts.md"}`),
		},
		"skills.files": {
			[]byte(`{"name":"deploy"}`),
		},
		"skills.list": {
			[]byte(`{}`),
		},
		"skills.remove": {
			[]byte(`{"name":"deploy"}`),
		},
		"skills.setEnabled": {
			[]byte(`{"name":"deploy","enabled":true}`),
		},
		"skills.approve": {
			[]byte(`{"name":"deploy"}`),
		},
		"shell.commandNames": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"shell.complete": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","cwd":"/tmp","line":"echo","pos":4}`),
		},
		"shell.footprint.consent": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"shell.footprint.helperUninstall": {
			[]byte(`{"profileId":"profile-1","fingerprint":"SHA256:abc","path":"~/.nocx/helper"}`),
		},
		"shell.footprint.status": {
			[]byte(`{}`),
		},
		"shell.footprint.uninstall": {
			[]byte(`{"profileId":"profile-1"}`),
		},
		"shell.integrate": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"shell.openUrl": {
			[]byte(`{"url":"https://example.com"}`),
		},
		// The heartbeat asks nothing and carries nothing: the client sends
		// `{}` (frontend/src/dispatcher.ts), which is the only shape worth
		// probing for a noParams() method.
		"transport.ping": {
			[]byte(`{}`),
		},
		"tunnel.open": {
			[]byte(`{"profileId":"profile-1","destination":"example.com:22"}`),
		},
		"tunnel.stop": {
			[]byte(`{"id":"0123456789abcdef0123456789abcdef"}`),
		},
		"uistate.get": {
			[]byte(`{}`),
		},
		"uistate.set": {
			[]byte(`{"sidebar":{"activeViewId":"home","width":240},"activeTab":"tab-1"}`),
		},
		"agent.approve": {
			[]byte(`{"runId":"run-1","attempt":1,"tool":"session.run","callId":"call-1","argHash":"hash","scope":"once"}`),
		},
		"agent.ask": {
			[]byte(`{"askId":"ask-1","sessionId":"session-1","question":"What happened?","attachedContent":[{"itemId":"item-1","command":"echo hi","state":"exited"}],"cwd":"/tmp"}`),
			[]byte(`{"askId":"ask-2","sessionId":"session-1","question":"Show output","attachedContent":[{"itemId":"item-2","command":"printf hi","state":"running","start":0,"count":20}],"cwd":"/tmp"}`),
		},
		"agent.cancel": {
			[]byte(`{"runId":1}`),
		},
		"agent.dump": {
			[]byte(`{"entryId":"entry-1"}`),
		},
		"agent.laneInteractivity": {
			[]byte(`{"sessionId":"session-1","bufferKind":"normal"}`),
		},
		"agent.readScreenResolved": {
			[]byte(`{"requestId":"request-1","outcome":"failed","error":"capture failed"}`),
		},
		"agent.runResolved": {
			[]byte(`{"requestId":"request-1","outcome":"failed","error":"run failed"}`),
		},
		"agent.status": {
			[]byte(`{}`),
		},
		"api.collections.close": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef"}`),
		},
		"api.collections.create": {
			[]byte(`{"name":"Local"}`),
		},
		"api.collections.createFolder": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","name":"folder"}`),
		},
		"api.collections.list": {
			[]byte(`{}`),
		},
		"api.collections.open": {
			[]byte(`{"path":"/tmp"}`),
		},
		"api.environment.read": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"environments/dev.json"}`),
		},
		"api.environment.write": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"environments/dev.json","environment":{"name":"dev","values":{},"route":{"kind":"direct","profileId":"","insecureTls":false}}}`),
		},
		"api.folder.read": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"folder"}`),
		},
		"api.folder.write": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"folder","variables":[]}`),
		},
		"api.import.curl": {
			[]byte(`{"line":"curl https://example.com"}`),
		},
		"api.import.postman": {
			[]byte(`{"document":"{}","dest":"/tmp"}`),
		},
		"api.request.cancel": {
			[]byte(`{"token":"token-1"}`),
		},
		"rpc.cancel": {
			[]byte(`{"id":1}`),
			[]byte(`{"id":"request-1"}`),
		},
		"api.request.delete": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"requests/get.json"}`),
		},
		"api.request.move": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"requests/get.json","toRelPath":"requests/post.json"}`),
		},
		"api.request.read": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"requests/get.json"}`),
		},
		"api.request.scope": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"requests/get.json","envRelPath":"","variables":[]}`),
		},
		"api.request.send": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"requests/get.json","token":"token-1"}`),
		},
		"api.request.write": {
			[]byte(`{"handle":"0123456789abcdef0123456789abcdef","relPath":"requests/get.json","request":{"id":"get","name":"Get","method":"GET","url":"https://example.com","headers":[],"query":[],"variables":[],"body":{"kind":"none","text":"","fileRef":""},"auth":{"kind":"none","user":"","token":"","password":""}}}`),
		},
		"files.close": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"files.download": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","path":"/tmp/file.txt"}`),
		},
		"files.downloadCancel": {
			[]byte(`{"transferId":"0123456789abcdef0123456789abcdef"}`),
		},
		"files.list": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","path":"/tmp","offset":0,"limit":20}`),
		},
		"files.open": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"files.read": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","path":"/tmp/file.txt","maxBytes":0}`),
		},
		"files.stat": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","path":"/tmp/file.txt"}`),
		},
		"files.reveal": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","path":"/tmp/file.txt"}`),
		},
		"files.upload": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","destDir":"/tmp","name":"file.txt","size":0}`),
		},
		"files.uploadCancel": {
			[]byte(`{"transferId":"0123456789abcdef0123456789abcdef"}`),
		},
		// Both values, because the flag is the whole method: a probe set that
		// only ever says "true" would accept a schema that had made the field
		// a constant.
		"files.visible": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","visible":true}`),
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","visible":false}`),
		},
		"files.watch": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","paths":[]}`),
		},
		"git.close": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"git.commit": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","message":"update","amend":false}`),
		},
		"git.diff": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","path":"file.txt","side":"unstaged","maxBytes":1024}`),
		},
		"git.headMessage": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"git.log": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"git.open": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","cwd":"/tmp"}`),
		},
		"git.remote": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"git.stage": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","paths":[]}`),
		},
		"git.stageAll": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"git.status": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"git.unstage": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef","paths":[]}`),
		},
		"git.unstageAll": {
			[]byte(`{"bindingId":"0123456789abcdef0123456789abcdef"}`),
		},
		"secrets.captureDismiss": {
			[]byte(`{"captureId":"cap_0123456789abcdef0123456789abcdef"}`),
		},
		"secrets.captureSave": {
			[]byte(`{"captureId":"cap_0123456789abcdef0123456789abcdef"}`),
		},
		"secrets.detect": {
			[]byte(`{"line":"echo hello","revision":1}`),
		},
		"secrets.paneClosed": {
			[]byte(`{"paneId":"pane-1"}`),
		},
		"secrets.saveKeyMaterial": {
			[]byte(`{"keyText":"private-key"}`),
		},
		"secrets.saveKeyPassphrase": {
			[]byte(`{"keyRow":"secrow:0123456789abcdef0123456789abcdef","passphrase":""}`),
		},
		"secrets.savePassword": {
			[]byte(`{"password":"password"}`),
		},
		"secrets.usage": {
			[]byte(`{"row":"secrow:0123456789abcdef0123456789abcdef"}`),
		},
		"vault.activity": {
			[]byte(`{}`),
		},
		"vault.changePassphrase": {
			[]byte(`{"oldPassphrase":"old","newPassphrase":"new"}`),
		},
		"vault.createSecret": {
			[]byte(`{"name":"token","kind":"api-token","value":""}`),
		},
		"vault.deleteSecret": {
			[]byte(`{"id":"secrow:0123456789abcdef0123456789abcdef"}`),
		},
		"vault.inventory": {
			[]byte(`{}`),
		},
		"vault.regenerateRecovery": {
			[]byte(`{"passphrase":"passphrase"}`),
		},
		"vault.renameSecret": {
			[]byte(`{"id":"secrow:0123456789abcdef0123456789abcdef","name":"renamed"}`),
		},
		"vault.replaceSecret": {
			[]byte(`{"id":"secrow:0123456789abcdef0123456789abcdef","value":""}`),
		},
		"vault.reset": {
			[]byte(`{}`),
		},
		"vault.resetPreview": {
			[]byte(`{}`),
		},
		"vault.resolveLine": {
			[]byte(`{"line":"echo hello"}`),
		},
		"vault.seal": {
			[]byte(`{}`),
		},
		"vault.setAutoSeal": {
			[]byte(`{"minutes":0}`),
		},
		"vault.setDefaultProvider": {
			[]byte(`{"provider":"file"}`),
		},
		"vault.setup": {
			[]byte(`{"passphrase":"hunter2"}`),
		},
		"vault.status": {
			[]byte(`{}`),
		},
		"vault.unlockResolved": {
			[]byte(`{"requestId":"request-1","outcome":"cancelled"}`),
		},
		"vault.unseal": {
			[]byte(`{"means":"passphrase","secret":"hunter2"}`),
		},
		"open": {
			[]byte(`{"cols":80,"rows":24}`),
		},
		"resize": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","cols":80,"rows":24}`),
		},
		"close": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"attach": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","offset":0}`),
		},
		"ack": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","offset":0}`),
		},
		"sshConfig.aliases": {
			[]byte(`{}`),
			[]byte(`null`),
		},
		"sshConfig.path": {
			[]byte(`{}`),
			[]byte(`null`),
		},
		"layout.read": {
			[]byte(`{}`),
			[]byte(`null`),
		},
		"workspaces.create": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c001","name":"Workspace","firstTab":{"id":"0198f2b0-0000-7000-8000-00000000c002","layout":"row"},"firstPane":{"id":"0198f2b0-0000-7000-8000-00000000c003","kind":"local","sizeShare":1}}`),
		},
		"workspaces.rename": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c001","name":"Workspace"}`),
		},
		"workspaces.recolour": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c001","colour":null}`),
		},
		"workspaces.reorder": {
			[]byte(`{"ids":["0198f2b0-0000-7000-8000-00000000c001"]}`),
		},
		"workspaces.close": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c001"}`),
		},
		"tabs.create": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c004","workspaceId":"workspace:default","layout":"column","firstPane":{"id":"0198f2b0-0000-7000-8000-00000000c005","kind":"local","sizeShare":1}}`),
		},
		"tabs.rename": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c004","name":null}`),
		},
		"tabs.recolour": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c004","colour":null}`),
		},
		"tabs.pin": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c004"}`),
		},
		"tabs.reorder": {
			[]byte(`{"workspaceId":"workspace:default","ids":["0198f2b0-0000-7000-8000-00000000c004"]}`),
		},
		"tabs.close": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c004"}`),
		},
		"panes.create": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c006","tabId":"0198f2b0-0000-7000-8000-00000000c004","kind":"local","sizeShare":1}`),
		},
		"panes.move": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c006","tabId":"0198f2b0-0000-7000-8000-00000000c004"}`),
		},
		"panes.setCwd": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c006","cwd":"/tmp"}`),
		},
		"panes.close": {
			[]byte(`{"id":"0198f2b0-0000-7000-8000-00000000c006"}`),
		},
		// A fresh client asks what is live and narrows it by nothing, so the
		// empty object and the absent params are the whole valid set
		// (nocx-oevq4).
		"sessions.live": {
			[]byte(`{}`),
			[]byte(`null`),
		},
		"sessions.inventory": {
			[]byte(`{}`),
			[]byte(`null`),
		},
		// The id alone is enough to read; instanceId and sessionEpoch are the
		// claim a RECLAIMING client makes about which incarnation it means,
		// and a client that was never told either still reads (nocx-22k1c.2).
		"session.output": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","from":4096}`),
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef","instanceId":"fedcba9876543210fedcba9876543210","sessionEpoch":1,"from":0}`),
		},
		// One probe per outcome, because the outcome is what decides which
		// other field may appear at all (nocx-uo1k6).
		"host.resolved": {
			[]byte(`{"outcome":"ok","path":"/tmp/chosen"}`),
			[]byte(`{"outcome":"cancelled"}`),
			[]byte(`{"outcome":"failed","error":"the dialog could not be presented"}`),
			[]byte(`{"outcome":"unavailable"}`),
		},
		"host.attentionActivated": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
		"detach": {
			[]byte(`{"sessionId":"0123456789abcdef0123456789abcdef"}`),
		},
	}
	for method := range registered {
		if _, ok := valid[method]; !ok {
			t.Errorf("registered method %s has no valid params probes", method)
		}
	}
	for method := range valid {
		if _, ok := registered[method]; !ok {
			t.Errorf("valid params probes have no registered method %s", method)
		}
	}

	for method, spec := range registered {
		name := method + ".params.schema.json"
		if _, ok := contracts[method]; !ok {
			t.Errorf("registered method %s has no params contract", method)
			continue
		}
		schema := loadSchema(t, name)
		probes := invalid
		switch method {
		case "secrets.detect", "vault.resolveLine":
			// These methods intentionally accept an empty object and use a
			// tolerant decoder for optional input; malformed JSON values
			// remain the shared shape rejection for them.
			probes = [][]byte{[]byte(`[]`), []byte(`"scalar"`), []byte(`true`)}
		case "rpc.cancel":
			// JSON-RPC permits string and number ids, so the shared id-length
			// and numeric probes are valid for this method.
			probes = [][]byte{
				[]byte(`[]`),
				[]byte(`"scalar"`),
				[]byte(`true`),
				[]byte(`{"unknown":true}`),
				[]byte(`{"id":null}`),
				[]byte(`{"id":{}}`),
			}
		}
		for _, raw := range probes {
			raw := raw
			t.Run(method+"/"+string(raw), func(t *testing.T) {
				if err := validateJSONErr(schema, raw); err == nil {
					t.Fatalf("schema accepted invalid probe %s", raw)
				}
				if msg := spec.validate(raw); msg == "" {
					t.Fatalf("registered validator accepted schema-rejected probe %s", raw)
				}
			})
		}
		for _, raw := range valid[method] {
			raw := raw
			t.Run(method+"/accept/"+string(raw), func(t *testing.T) {
				if err := validateJSONErr(schema, raw); err != nil {
					t.Fatalf("schema rejected valid probe %s: %v", raw, err)
				}
				if msg := spec.validate(raw); msg != "" {
					t.Fatalf("registered validator rejected schema-accepted probe %s: %s", raw, msg)
				}
			})
		}
	}

	for method := range registered {
		if _, ok := contracts[method]; !ok {
			continue
		}
		path := filepath.Join(contractDir, method+".params.schema.json")
		raw, readErr := os.ReadFile(path) //nolint:gosec // path is registration-derived under contracts/
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		var metadata struct {
			AdditionalProperties *bool    `json:"additionalProperties"`
			Required             []string `json:"required"`
		}
		if parseErr := json.Unmarshal(raw, &metadata); parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		if metadata.AdditionalProperties == nil || *metadata.AdditionalProperties {
			t.Errorf("%s must set additionalProperties to false", path)
		}
		if metadata.Required == nil {
			t.Errorf("%s must declare required, including when it is empty", path)
		}
	}
}

func TestDecodeObjectRejectsUnknownTrailingAndNamedNull(t *testing.T) {
	type params struct {
		Required string `json:"required"`
	}
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "malformed object", raw: json.RawMessage(`{`), want: "params must be a JSON object"},
		{name: "top-level null", raw: json.RawMessage(`null`)},
		{name: "empty object", raw: json.RawMessage(`{}`)},
		{name: "named value", raw: json.RawMessage(`{"required":"ok"}`)},
		{name: "named null", raw: json.RawMessage(`{"required":null}`), want: "required must not be null"},
		{name: "unknown field", raw: json.RawMessage(`{"unknown":true}`), want: `unknown field "unknown"`},
		{name: "trailing value", raw: json.RawMessage(`{} {}`), want: "trailing content after the params object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got params
			if msg := decodeObject(test.raw, &got, "required"); msg != test.want {
				t.Fatalf("decodeObject(%s) = %q, want %q", test.raw, msg, test.want)
			}
		})
	}
}
