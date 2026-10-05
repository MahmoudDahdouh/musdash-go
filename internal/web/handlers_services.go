package web

import (
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/catalog"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/deploy"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// maxComposeBytes bounds a Compose file a person pastes.
const maxComposeBytes = 128 << 10

// loadService fetches the service in the path with its project,
// environment and endpoints, for the signed-in team. It answers 404 itself.
func (s *Server) loadService(w http.ResponseWriter, r *http.Request) (pages.ServiceView, bool) {
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	var v pages.ServiceView
	svc, err := s.DB.Service(ctx, teamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return v, false
	}
	if err == nil {
		v.Service = svc
		v.Env, err = s.DB.Environment(ctx, teamID, svc.EnvironmentID)
	}
	if err == nil {
		v.Project, err = s.DB.Project(ctx, teamID, v.Env.ProjectID)
	}
	if err == nil {
		v.Endpoints, err = s.DB.ListEndpoints(ctx, svc.ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return v, false
	}
	return v, true
}

func (s *Server) serviceShell(w http.ResponseWriter, r *http.Request, v pages.ServiceView) ui.Shell {
	return s.shell(w, r, v.Service.Name, "projects",
		envCrumbs(v.Project, v.Env, resourceCrumb(v.Env, db.KindService, v.Service.ID, v.Service.Name))...,
	)
}

// serviceTemplate resolves the template of a new service: a catalogue entry
// or the empty one for a person's own file.
func serviceTemplate(key string) (catalog.ServiceTemplate, bool) {
	switch key {
	case db.TemplateCustom:
		return catalog.ServiceTemplate{Key: "", Name: "Your own Compose file"}, true
	case db.TemplateGit:
		return catalog.ServiceTemplate{Key: "", Name: "A Compose file in a Git repository"}, true
	}
	return catalog.Service(key)
}

// requiredVars lists the variables of a Compose file a person has to
// supply.
func requiredVars(composeText string) []catalog.VarRef {
	var out []catalog.VarRef
	for _, v := range catalog.ScanVariables(composeText) {
		if v.Required {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) serviceNew(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.loadProjectEnv(w, r)
	if !ok {
		return
	}
	key := r.URL.Query().Get("template")
	if key == "" {
		// The templates are on the page every kind of resource is added from.
		redirect(w, r, envPath(p.ID, env.ID)+"/new")
		return
	}
	shell := s.shell(w, r, "New service", "projects", newResourceCrumbs(p, env, "Service")...)
	tpl, known := serviceTemplate(key)
	if !known {
		s.notFound(w, r)
		return
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	serverList, err := s.serverChoices(r.Context(), sessionFrom(r).TeamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.ServiceNew(shell, p, env, key, tpl, requiredVars(tpl.Compose), ui.Form{}, choices, serverList))
}

// parseServiceSource reads the repository fields of a service from Git.
func parseServiceSource(r *http.Request, f *ui.Form, c pages.GitChoices, svc *db.Service) {
	var repo db.App
	parseRepoForm(r, f, c, &repo)
	svc.RepoURL, svc.RepoName, svc.Branch = repo.RepoURL, repo.RepoName, repo.Branch
	svc.GitSourceID, svc.SSHKeyID, svc.AutoDeploy = repo.GitSourceID, repo.SSHKeyID, repo.AutoDeploy
	svc.ComposePath = strings.Trim(strings.TrimSpace(r.PostFormValue("compose_path")), "/")
	f.Set("compose_path", svc.ComposePath)
	if svc.ComposePath == "" || !source.ValidRelPath(svc.ComposePath) {
		f.Fail("compose_path", "Enter the file's path inside the repository, such as docker-compose.yml.")
	}
}

// parseServiceVariables reads the Variables box: the values a person
// supplies for a Compose file. Names musdash fills in itself are refused,
// so a typed value never silently loses to a generated one.
func parseServiceVariables(f *ui.Form, text string) map[string]string {
	vars := map[string]string{}
	list, err := deploy.ParseEnv(text)
	if err == nil {
		_, err = deploy.EnvFile(list)
	}
	if err != nil {
		f.Fail("variables", sentence(err))
		return vars
	}
	for _, v := range list {
		if _, magic := catalog.ParseMagic(v.Key); magic {
			f.Fail("variables", v.Key+" is filled in by musdash. Remove it from the variables, or use another name in the file.")
			continue
		}
		vars[v.Key] = v.Value
	}
	return vars
}

// checkCompose applies the checks a Compose file can be given without
// loading it. Whether it is valid, and allowed, is found out when it is
// deployed, in the sandbox.
func checkCompose(f *ui.Form, text string) {
	switch {
	case strings.TrimSpace(text) == "":
		f.Fail("compose", "Paste a Compose file.")
	case len(text) > maxComposeBytes:
		f.Fail("compose", "The file is larger than 128 KB.")
	case strings.ContainsRune(text, 0):
		f.Fail("compose", "The file contains a character that cannot be part of a Compose file.")
	}
}

func (s *Server) serviceCreate(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.loadProjectEnv(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	teamID := sessionFrom(r).TeamID
	key := r.PostFormValue("template")
	tpl, known := serviceTemplate(key)
	if !known {
		s.notFound(w, r)
		return
	}
	serverList, err := s.serverChoices(ctx, teamID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var f ui.Form
	f.Set("_submitted", "1")
	server := targetServer(r, &f, serverList)
	svc := db.Service{
		EnvironmentID: env.ID, ServerID: server.ID, Template: key,
		Name:       strings.ToLower(strings.TrimSpace(r.PostFormValue("name"))),
		Compose:    tpl.Compose,
		ConnectEnv: r.PostFormValue("connect_env") == "1",
	}
	f.Set("name", svc.Name)
	f.Set("connect_env", r.PostFormValue("connect_env"))
	f.Set("deploy", r.PostFormValue("deploy"))
	if !envNameRE.MatchString(svc.Name) {
		f.Fail("name", appNameRule)
	}
	vars := map[string]string{}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if key == db.TemplateGit {
		// The file is read when the service is deployed; until then
		// there is nothing to show or check.
		svc.Compose = ""
		parseServiceSource(r, &f, choices, &svc)
		f.Set("variables", r.PostFormValue("variables"))
		vars = parseServiceVariables(&f, r.PostFormValue("variables"))
	} else if key == db.TemplateCustom {
		// Line ends as a browser sends them, made the file's own.
		svc.Compose = strings.ReplaceAll(r.PostFormValue("compose"), "\r\n", "\n")
		f.Set("compose", svc.Compose)
		f.Set("variables", r.PostFormValue("variables"))
		checkCompose(&f, svc.Compose)
		vars = parseServiceVariables(&f, r.PostFormValue("variables"))
	} else {
		for _, v := range requiredVars(tpl.Compose) {
			value := strings.TrimSpace(r.PostFormValue("var_" + v.Name))
			f.Set("var_"+v.Name, value)
			if value == "" || strings.ContainsAny(value, "\n\r") {
				f.Fail("var_"+v.Name, "Enter a value on one line.")
			}
			vars[v.Name] = value
		}
	}
	rerender := func() {
		shell := s.shell(w, r, "New service", "projects", newResourceCrumbs(p, env, "Service")...)
		s.render(w, r, http.StatusUnprocessableEntity, pages.ServiceNew(shell, p, env, key, tpl, requiredVars(tpl.Compose), f, choices, serverList))
	}
	if !f.OK() {
		rerender()
		return
	}
	if svc.Variables, err = s.Deploy.SealServiceVariables(vars); err != nil {
		s.fail(w, r, err)
		return
	}
	name := svc.Name
	svc, err = s.DB.CreateService(ctx, teamID, svc)
	if errors.Is(err, db.ErrNotFound) {
		// A GitHub App or key that is not the team's.
		f.Fail("access", "Choose how the repository is read.")
		rerender()
		return
	}
	if errors.Is(err, db.ErrNameTaken) || db.IsUnique(err) {
		f.Fail("name", "This environment already has an app, database or service called "+name+".")
		rerender()
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Generated values, and an address that needs no DNS for each endpoint.
	if err := s.Deploy.PrepareService(ctx, svc, vars, func(string) (string, bool) { return generatedDomain(server), false }); err != nil {
		s.fail(w, r, err)
		return
	}
	if r.PostFormValue("deploy") == "1" {
		if err := s.Deploy.EnqueueService(ctx, svc, false); err != nil {
			s.Log.Error("queue service deployment", "service", svc.ID, "err", err)
		}
	} else {
		setFlash(w, r, ui.ToneOK, "Service created. Choose Deploy to start it.")
	}
	redirect(w, r, "/services/"+svc.ID)
}

// serviceValues splits a service's stored variables into the generated
// ones and the ones a person entered, and lists the required ones that
// have no value.
func (s *Server) serviceValues(svc db.Service) (generated []pages.Generated, entered []db.EnvVar, missing []string, err error) {
	vars, err := s.Deploy.ServiceVariables(svc)
	if err != nil {
		return nil, nil, nil, err
	}
	for name, value := range vars {
		if _, magic := catalog.ParseMagic(name); magic {
			generated = append(generated, pages.Generated{Name: name, Value: value})
		} else {
			entered = append(entered, db.EnvVar{Key: name, Value: value})
		}
	}
	sort.Slice(generated, func(i, j int) bool { return generated[i].Name < generated[j].Name })
	sort.Slice(entered, func(i, j int) bool { return entered[i].Key < entered[j].Key })
	for _, v := range requiredVars(svc.Compose) {
		if vars[v.Name] == "" {
			missing = append(missing, v.Name)
		}
	}
	return generated, entered, missing, nil
}

func (s *Server) serviceOverview(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	// The one page a service's generated values are opened on.
	generated, _, missing, err := s.serviceValues(v.Service)
	if err != nil {
		generated, missing = nil, nil
		if v.Service.LastError == "" {
			v.Service.Status, v.Service.LastError = db.AppFailed, sentence(err)
		}
	}
	s.render(w, r, http.StatusOK, pages.ServiceOverview(s.serviceShell(w, r, v), v, generated, missing))
}

// refreshWhenSettled tells a page whose header was polling a deployment
// to reload once the deployment is over: the rest of the page (addresses,
// containers, what went wrong) was drawn before it finished.
func refreshWhenSettled(w http.ResponseWriter, r *http.Request, status string) {
	if r.URL.Query().Get("was") == db.AppDeploying && status != db.AppDeploying {
		w.Header().Set("HX-Refresh", "true")
	}
}

// serviceStatus serves the header fragment that service pages poll.
func (s *Server) serviceStatus(w http.ResponseWriter, r *http.Request) {
	svc, err := s.DB.Service(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	refreshWhenSettled(w, r, svc.Status)
	s.render(w, r, http.StatusOK, pages.ServiceHeader(sessionFrom(r).CSRFToken, svc))
}

func (s *Server) serviceDeploy(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	s.queueServiceDeploy(w, r, v.Service, false)
	redirect(w, r, "/services/"+v.Service.ID)
}

// queueServiceDeploy queues a deployment and says so, or why not.
func (s *Server) queueServiceDeploy(w http.ResponseWriter, r *http.Request, svc db.Service, again bool) {
	err := s.Deploy.EnqueueService(r.Context(), svc, again)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "The service is already being deployed.")
	case err != nil:
		s.Log.Error("queue service deployment", "service", svc.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The deployment could not be queued. Try again.")
	}
}

func (s *Server) serviceStop(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	err := s.Deploy.StopService(r.Context(), v.Service.ID)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "The service is being deployed. Stop it once that has finished.")
	case err != nil:
		s.Log.Error("stop service", "service", v.Service.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The service could not be stopped: "+err.Error())
	default:
		setFlash(w, r, ui.ToneOK, "Service stopped. Its data is kept.")
	}
	redirect(w, r, "/services/"+v.Service.ID)
}

// serviceDeployLog streams the output of the service's latest deployment:
// all of it at once when the deployment is over, as it is written while it
// runs.
func (s *Server) serviceDeployLog(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	release, ok := s.takeStream(w)
	if !ok {
		return
	}
	defer release()
	ctx, stop := s.streamContext(r)
	defer stop()
	out := startSSE(w)
	finished := func() bool {
		cur, err := s.DB.ServiceByID(ctx, v.Service.ID)
		return err != nil || cur.Status != db.AppDeploying
	}
	if err := deploy.FollowWhenReady(ctx, s.Cfg.ServiceLogPath(v.Service.ID), out, finished); err == nil {
		out.finish()
	}
}

func (s *Server) serviceLogs(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadService(w, r); ok {
		s.render(w, r, http.StatusOK, pages.ServiceLogs(s.serviceShell(w, r, v), v))
	}
}

// serviceLogsStream streams the output of the stack's containers.
func (s *Server) serviceLogsStream(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	if v.Service.Members == "" || v.Service.Status == db.AppStopped {
		http.Error(w, "Nothing is running.", http.StatusConflict)
		return
	}
	server, err := s.DB.ServerByID(r.Context(), v.Service.ServerID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	run, err := s.Pool.Runner(r.Context(), server)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	release, ok := s.takeStream(w)
	if !ok {
		return
	}
	defer release()
	ctx, stop := s.streamContext(r)
	defer stop()
	out := startSSE(w)
	// Returns when the containers stop or the stream is cancelled.
	s.Deploy.ServiceLogs(ctx, run, v.Service.ID, 200, out)
	if ctx.Err() == nil {
		out.finish()
	}
}

func (s *Server) renderServiceCompose(w http.ResponseWriter, r *http.Request, status int, v pages.ServiceView, f ui.Form) {
	composeText, variables := v.Service.Compose, ""
	if _, submitted := f.Values["compose"]; submitted {
		composeText, variables = f.V("compose"), f.V("variables")
	} else if _, entered, _, err := s.serviceValues(v.Service); err == nil {
		variables = deploy.FormatEnv(entered)
	}
	s.renderServiceComposeGit(w, r, status, v, f, composeText, variables, ui.Form{}, "")
}

// renderServiceComposeGit is renderServiceCompose with the state of the
// forms only a service from Git has.
func (s *Server) renderServiceComposeGit(w http.ResponseWriter, r *http.Request, status int, v pages.ServiceView, f ui.Form, composeText, variables string, src ui.Form, newToken string) {
	var git *pages.ServiceGit
	if v.Service.FromGit() {
		choices, err := s.gitChoices(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		base := s.publicBase(r)
		git = &pages.ServiceGit{Choices: choices, Source: src, Tr: pages.Triggers{
			WebhookURL:   base + "/webhooks/git/" + v.Service.ID,
			DeployURL:    base + "/api/v1/deploy?uuid=" + v.Service.ID,
			HasToken:     v.Service.DeployTokenHash != "",
			NewToken:     newToken,
			ViaGitHubApp: v.Service.GitSourceID != "",
		}}
		if v.Service.WebhookSecret != "" {
			plain, err := s.Box.OpenString(v.Service.WebhookSecret)
			if err != nil {
				s.fail(w, r, errors.New("the webhook secret cannot be decrypted"))
				return
			}
			git.Tr.WebhookSecret = plain
		}
	}
	s.render(w, r, status, pages.ServiceCompose(s.serviceShell(w, r, v), v, f, composeText, variables, git))
}

// serviceSourceSave stores where a Git service's Compose file comes from.
func (s *Server) serviceSourceSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	if !v.Service.FromGit() {
		s.notFound(w, r)
		return
	}
	choices, err := s.gitChoices(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var f ui.Form
	svc := v.Service
	parseServiceSource(r, &f, choices, &svc)
	if f.OK() {
		err := s.DB.UpdateServiceSource(r.Context(), sessionFrom(r).TeamID, svc)
		switch {
		case errors.Is(err, db.ErrNotFound):
			f.Fail("access", "Choose how the repository is read.")
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		_, entered, _, _ := s.serviceValues(v.Service)
		s.renderServiceComposeGit(w, r, http.StatusUnprocessableEntity, v, ui.Form{}, v.Service.Compose, deploy.FormatEnv(entered), f, "")
		return
	}
	setFlash(w, r, ui.ToneOK, "Source saved. Deploy to read the file from there.")
	redirect(w, r, "/services/"+svc.ID+"/compose")
}

func (s *Server) serviceWebhookSecret(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	if !v.Service.FromGit() {
		s.notFound(w, r)
		return
	}
	sealed, err := s.Box.SealString(secret.RandomHex(24))
	if err == nil {
		err = s.DB.SetServiceWebhookSecret(r.Context(), sessionFrom(r).TeamID, v.Service.ID, sealed)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setFlash(w, r, ui.ToneOK, "Webhook secret saved. Enter it in the repository's webhook settings.")
	redirect(w, r, "/services/"+v.Service.ID+"/compose#triggers")
}

// serviceDeployToken creates, replaces or revokes the service's deploy
// token. A new token is shown once: only its hash is kept.
func (s *Server) serviceDeployToken(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	if !v.Service.FromGit() {
		s.notFound(w, r)
		return
	}
	teamID := sessionFrom(r).TeamID
	if r.PostFormValue("revoke") == "1" {
		if err := s.DB.SetServiceDeployToken(r.Context(), teamID, v.Service.ID, ""); err != nil {
			s.fail(w, r, err)
			return
		}
		setFlash(w, r, ui.ToneOK, "Deploy token revoked.")
		redirect(w, r, "/services/"+v.Service.ID+"/compose#triggers")
		return
	}
	token := "mdt_" + secret.RandomToken(32)
	hash := secret.HashToken(token)
	if err := s.DB.SetServiceDeployToken(r.Context(), teamID, v.Service.ID, hash); err != nil {
		s.fail(w, r, err)
		return
	}
	v.Service.DeployTokenHash = hash
	_, entered, _, _ := s.serviceValues(v.Service)
	// Rendered directly rather than after a redirect, so the token is never
	// placed in a cookie or a URL.
	s.renderServiceComposeGit(w, r, http.StatusOK, v, ui.Form{}, v.Service.Compose, deploy.FormatEnv(entered), ui.Form{}, token)
}

func (s *Server) serviceCompose(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadService(w, r); ok {
		s.renderServiceCompose(w, r, http.StatusOK, v, ui.Form{})
	}
}

func (s *Server) serviceComposeSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var f ui.Form
	f.Set("_submitted", "1")
	svc := v.Service
	svc.ConnectEnv = r.PostFormValue("connect_env") == "1"
	if !svc.FromGit() {
		// A service from Git keeps the file its last deployment read.
		svc.Compose = strings.ReplaceAll(r.PostFormValue("compose"), "\r\n", "\n")
		checkCompose(&f, svc.Compose)
	}
	f.Set("compose", svc.Compose)
	f.Set("variables", r.PostFormValue("variables"))
	f.Set("connect_env", r.PostFormValue("connect_env"))
	entered := parseServiceVariables(&f, r.PostFormValue("variables"))
	if !f.OK() {
		s.renderServiceCompose(w, r, http.StatusUnprocessableEntity, v, f)
		return
	}
	typed := make([]string, 0, len(entered))
	for _, value := range entered {
		typed = append(typed, value)
	}
	// What was generated before is kept; what the person entered replaces
	// what they had entered.
	stored, err := s.Deploy.ServiceVariables(v.Service)
	if err != nil {
		f.Fail("variables", sentence(err))
		s.renderServiceCompose(w, r, http.StatusUnprocessableEntity, v, f)
		return
	}
	vars := entered
	for name, value := range stored {
		if _, magic := catalog.ParseMagic(name); magic {
			vars[name] = value
		}
	}
	if svc.Variables, err = s.Deploy.SealServiceVariables(vars); err != nil {
		s.fail(w, r, err)
		return
	}
	if svc.FromGit() {
		// The file and what follows from it, the endpoints and generated
		// values, are a deployment's business: it reads the repository.
		// Writing back the text this page was loaded with could undo what
		// a deployment running right now has just recorded.
		if err := s.DB.UpdateServiceSettings(ctx, sessionFrom(r).TeamID, svc); err != nil {
			s.fail(w, r, err)
			return
		}
		if r.PostFormValue("deploy") == "1" {
			s.queueServiceDeploy(w, r, svc, true)
			redirect(w, r, "/services/"+svc.ID)
			return
		}
		s.warnMissingShared(w, r, svc.EnvironmentID, svc.ServerID, typed, "Saved. Deploy to apply it.")
		redirect(w, r, "/services/"+svc.ID+"/compose")
		return
	}
	if err := s.DB.UpdateServiceCompose(ctx, sessionFrom(r).TeamID, svc); err != nil {
		s.fail(w, r, err)
		return
	}
	server, err := s.DB.ServerByID(ctx, svc.ServerID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Deploy.PrepareService(ctx, svc, vars, func(string) (string, bool) { return generatedDomain(server), false }); err != nil {
		s.fail(w, r, err)
		return
	}
	if r.PostFormValue("deploy") == "1" {
		s.queueServiceDeploy(w, r, svc, true)
		redirect(w, r, "/services/"+svc.ID)
		return
	}
	s.warnMissingShared(w, r, svc.EnvironmentID, svc.ServerID, typed, "Saved. Deploy to apply it.")
	redirect(w, r, "/services/"+svc.ID+"/compose")
}

// renderServiceSettings draws a service's Settings page. failed is the
// endpoint whose form was refused.
func (s *Server) renderServiceSettings(w http.ResponseWriter, r *http.Request, status int, v pages.ServiceView, failed string, f ui.Form) {
	var err error
	if v.Tags, err = s.DB.TagsOf(r.Context(), sessionFrom(r).TeamID, db.KindService, v.Service.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, pages.ServiceSettings(s.serviceShell(w, r, v), v, failed, f))
}

func (s *Server) serviceSettings(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadService(w, r); ok {
		s.renderServiceSettings(w, r, http.StatusOK, v, "", ui.Form{})
	}
}

// serviceEndpointSave gives one endpoint of a service its domain.
func (s *Server) serviceEndpointSave(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	endpointID := r.PathValue("eid")
	var current *db.Endpoint
	for i := range v.Endpoints {
		if v.Endpoints[i].ID == endpointID {
			current = &v.Endpoints[i]
		}
	}
	if current == nil {
		s.notFound(w, r)
		return
	}
	var f ui.Form
	raw := strings.TrimSpace(r.PostFormValue("host"))
	f.Set("host", raw)
	tls := r.PostFormValue("tls") == "1"
	back := "/services/" + v.Service.ID + "/settings"

	host := current.Host
	if !strings.EqualFold(raw, current.Host) {
		host = s.checkDomain(ctx, &f, "host", raw)
	}
	if f.OK() && tls && isGeneratedDomain(host) {
		f.Fail("host", "A generated address is served over plain HTTP. Untick HTTPS, or enter a domain of your own.")
	}
	if f.OK() {
		err := s.DB.SetEndpointDomain(ctx, v.Service.ID, current.ID, host, tls)
		switch {
		case db.IsUnique(err), errors.Is(err, db.ErrHostTaken):
			f.Fail("host", domainTaken)
		case errors.Is(err, db.ErrNotFound):
			s.notFound(w, r)
			return
		case err != nil:
			s.fail(w, r, err)
			return
		}
	}
	if !f.OK() {
		s.renderServiceSettings(w, r, http.StatusUnprocessableEntity, v, current.ID, f)
		return
	}
	// The new name is routed at once; the service itself learns its address
	// when it is next deployed.
	s.syncRoutes(r, v.Service.ServerID)
	if v.Service.Members != "" {
		setFlash(w, r, ui.ToneOK, "Domain saved and routed. Redeploy so the service knows its new address.")
	} else {
		setFlash(w, r, ui.ToneOK, "Domain saved.")
	}
	redirect(w, r, back)
}

func (s *Server) serviceDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	back := "/services/" + v.Service.ID + "/settings"
	if strings.TrimSpace(r.PostFormValue("confirm")) != v.Service.Name {
		setFlash(w, r, ui.ToneDanger, "The service was not deleted: the name you typed did not match.")
		redirect(w, r, back)
		return
	}
	deleteData := r.PostFormValue("delete_data") == "1"
	err := s.Deploy.DestroyService(r.Context(), v.Service.ID, deleteData)
	switch {
	case errors.Is(err, deploy.ErrBusy):
		setFlash(w, r, ui.ToneWarn, "The service is being deployed. Delete it once that has finished.")
		redirect(w, r, back)
		return
	case err != nil:
		s.Log.Error("delete service", "service", v.Service.ID, "err", err)
		setFlash(w, r, ui.ToneDanger, "The service could not be deleted: "+err.Error())
		redirect(w, r, back)
		return
	}
	os.Remove(s.Cfg.ServiceLogPath(v.Service.ID))
	if deleteData {
		setFlash(w, r, ui.ToneOK, "Service and its volumes deleted.")
	} else {
		setFlash(w, r, ui.ToneOK, "Service deleted. Its volumes are still on the server.")
	}
	redirect(w, r, envPath(v.Project.ID, v.Env.ID))
}
