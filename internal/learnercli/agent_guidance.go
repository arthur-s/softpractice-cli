package learnercli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

type AgentGuidanceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type AgentGuidance struct {
	SchemaVersion     int                 `json:"schema_version"`
	AssignmentID      string              `json:"assignment_id"`
	AssignmentVersion int                 `json:"assignment_version"`
	SHA256            string              `json:"sha256"`
	Files             []AgentGuidanceFile `json:"files"`
}

// GetAgentGuidance leaves legacy status responses unchanged. Old servers may
// lack the separate endpoint; authentication and transport failures still fail.
func (c *Client) GetAgentGuidance(ctx context.Context, assignmentID string, version int) (*AgentGuidance, error) {
	if !projectIDPattern.MatchString(assignmentID) || version < 1 {
		return nil, errors.New("invalid guidance assignment")
	}
	var result AgentGuidance
	err := c.AuthorizedJSON(ctx, "GET", fmt.Sprintf("/v1/assignments/%s/agent-guidance?version=%d", url.PathEscape(assignmentID), version), nil, &result)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := result.Validate(assignmentID, version); err != nil {
		return nil, err
	}
	return &result, nil
}

func (g AgentGuidance) Validate(assignmentID string, version int) error {
	if g.SchemaVersion != 1 || g.AssignmentID != assignmentID || g.AssignmentVersion != version || g.Files == nil {
		return errors.New("invalid agent guidance identity")
	}
	if len(g.Files) != 0 && len(g.Files) != 2 {
		return errors.New("invalid agent guidance files")
	}
	for i, file := range g.Files {
		if file.Path != []string{"AGENTS.md", "CLAUDE.md"}[i] || len(file.Content) == 0 || len(file.Content) > 64<<10 {
			return errors.New("invalid agent guidance file")
		}
	}
	data, err := json.Marshal(g.Files)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != g.SHA256 {
		return errors.New("agent guidance digest mismatch")
	}
	return nil
}
