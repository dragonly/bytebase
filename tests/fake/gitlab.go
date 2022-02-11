package fake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bytebase/bytebase/plugin/vcs/gitlab"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// GitLab is a fake implementation of GitLab.
type GitLab struct {
	port int
	Echo *echo.Echo

	client *http.Client

	nextWebhookID int
	projects      map[string]*projectData
}

type projectData struct {
	webhooks []*gitlab.WebhookPost
	files    map[string]string
}

// NewGitLab creates a fake GitLab.
func NewGitLab(port int) *GitLab {
	e := echo.New()
	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	gl := &GitLab{
		port:          port,
		Echo:          e,
		client:        &http.Client{},
		nextWebhookID: 20210113,
		projects:      map[string]*projectData{},
	}

	// Routes
	projectGroup := e.Group("/api/v4")
	projectGroup.POST("/projects/:id/hooks", gl.createProjectHook)
	projectGroup.GET("/projects/:id/repository/files/:file/raw", gl.readProjectFile)
	projectGroup.GET("/projects/:id/repository/files/:file", gl.readProjectFileMetadata)
	projectGroup.POST("/projects/:id/repository/files/:file", gl.createProjectFile)
	projectGroup.PUT("/projects/:id/repository/files/:file", gl.createProjectFile)

	return gl
}

// Run runs a GitLab server.
func (gl *GitLab) Run() error {
	return gl.Echo.Start(fmt.Sprintf(":%d", gl.port))
}

// Close close a GitLab server.
func (gl *GitLab) Close() error {
	return gl.Echo.Close()
}

// CreateProject creates a GitLab project.
func (gl *GitLab) CreateProject(id string) {
	gl.projects[id] = &projectData{
		files: map[string]string{},
	}
}

// createProjectHook creates a project webhook.
func (gl *GitLab) createProjectHook(c echo.Context) error {
	gitlabProjectID := c.Param("id")
	c.Logger().Info("create webhook for project %q", c.Param("id"))
	b, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return fmt.Errorf("failed to read create project hook request body, error %w", err)
	}
	webhookPost := &gitlab.WebhookPost{}
	if err := json.Unmarshal(b, webhookPost); err != nil {
		return fmt.Errorf("failed to unmarshal create project hook request body, error %w", err)
	}
	pd, ok := gl.projects[gitlabProjectID]
	if !ok {
		return fmt.Errorf("gitlab project %q doesn't exist", gitlabProjectID)
	}
	pd.webhooks = append(pd.webhooks, webhookPost)

	if err := json.NewEncoder(c.Response().Writer).Encode(&gitlab.WebhookInfo{
		ID: gl.nextWebhookID,
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to marshal WebhookInfo response").SetInternal(err)
	}
	gl.nextWebhookID++

	return nil
}

// createProjectHook creates a project webhook.
func (gl *GitLab) readProjectFile(c echo.Context) error {
	gitlabProjectID := c.Param("id")
	fileNameEscaped := c.Param("file")
	fileName, err := url.QueryUnescape(fileNameEscaped)
	if err != nil {
		return c.String(http.StatusBadRequest, fmt.Sprintf("failed to query unescape %q, error: %v", fileNameEscaped, err))
	}

	pd, ok := gl.projects[gitlabProjectID]
	if !ok {
		return c.String(http.StatusBadRequest, fmt.Sprintf("gitlab project %q doesn't exist", gitlabProjectID))
	}

	content, ok := pd.files[fileName]
	if !ok {
		return c.String(http.StatusNotFound, fmt.Sprintf("file %q not found", fileName))
	}

	return c.String(http.StatusOK, content)
}

// createProjectHook creates a project webhook.
func (gl *GitLab) readProjectFileMetadata(c echo.Context) error {
	gitlabProjectID := c.Param("id")
	fileNameEscaped := c.Param("file")
	fileName, err := url.QueryUnescape(fileNameEscaped)
	if err != nil {
		return c.String(http.StatusBadRequest, fmt.Sprintf("failed to query unescape %q, error: %v", fileNameEscaped, err))
	}

	pd, ok := gl.projects[gitlabProjectID]
	if !ok {
		return c.String(http.StatusBadRequest, fmt.Sprintf("gitlab project %q doesn't exist", gitlabProjectID))
	}

	if _, ok := pd.files[fileName]; !ok {
		return c.String(http.StatusNotFound, fmt.Sprintf("file %q not found", fileName))
	}

	buf, err := json.Marshal(&gitlab.FileMeta{})
	if err != nil {
		return c.String(http.StatusInternalServerError, fmt.Sprintf("failed to marshal FileMeta, error %v", err))
	}

	return c.String(http.StatusOK, string(buf))
}

// createProjectHook creates a project file.
func (gl *GitLab) createProjectFile(c echo.Context) error {
	gitlabProjectID := c.Param("id")
	fileNameEscaped := c.Param("file")
	fileName, err := url.QueryUnescape(fileNameEscaped)
	if err != nil {
		return c.String(http.StatusBadRequest, fmt.Sprintf("failed to query unescape %q, error: %v", fileNameEscaped, err))
	}
	b, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.String(http.StatusInternalServerError, fmt.Sprintf("failed to read create project file request body, error %v", err))
	}
	fileCommit := &gitlab.FileCommit{}
	if err := json.Unmarshal(b, fileCommit); err != nil {
		return c.String(http.StatusBadRequest, fmt.Sprintf("failed to unmarshal create project file request body, error %v", err))
	}

	pd, ok := gl.projects[gitlabProjectID]
	if !ok {
		return c.String(http.StatusBadRequest, fmt.Sprintf("gitlab project %q doesn't exist", gitlabProjectID))
	}

	// Save file.
	pd.files[fileName] = fileCommit.Content

	return c.String(http.StatusOK, "")
}

// SendCommits sends comments to webhooks.
func (gl *GitLab) SendCommits(gitlabProjectID string, webhookPushEvent *gitlab.WebhookPushEvent) error {
	pd, ok := gl.projects[gitlabProjectID]
	if !ok {
		return fmt.Errorf("gitlab project %q doesn't exist", gitlabProjectID)
	}

	// Trigger webhooks.
	for _, webhook := range pd.webhooks {
		// Send post request.
		buf, err := json.Marshal(webhookPushEvent)
		if err != nil {
			return fmt.Errorf("failed to marshal webhookPushEvent, error %w", err)
		}
		req, err := http.NewRequest("POST", webhook.URL, strings.NewReader(string(buf)))
		if err != nil {
			return fmt.Errorf("fail to create a new POST request(%q), error: %w", webhook.URL, err)
		}
		req.Header.Set("X-Gitlab-Token", webhook.SecretToken)
		resp, err := gl.client.Do(req)
		if err != nil {
			return fmt.Errorf("fail to send a POST request(%q), error: %w", webhook.URL, err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read http response body, error: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("http response error code %v body %q", resp.StatusCode, string(body))
		}
		gl.Echo.Logger.Infof("SendCommits response body %s\n", body)
	}

	return nil
}

// AddFiles add files to repository.
func (gl *GitLab) AddFiles(gitlabProjectID string, files map[string]string) error {
	pd, ok := gl.projects[gitlabProjectID]
	if !ok {
		return fmt.Errorf("gitlab project %q doesn't exist", gitlabProjectID)
	}

	// Save files
	for name, content := range files {
		pd.files[name] = content
	}
	return nil
}