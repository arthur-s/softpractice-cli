package main

import (
	"encoding/json"
	"fmt"
)

// reviewFeedbackDetail says how much of the reviewer's feedback the `evaluation`
// command prints, in both text and JSON.
type reviewFeedbackDetail string

const (
	// reviewFeedbackSummary prints the verdict, the deterministic check
	// results, how many findings the reviewer made, their priorities, and the
	// rubric levels. It leaves out every free-text part of the review: the
	// findings' titles, observations, risks, and directions, the rubric
	// rationales, the uncertainty reason, the defense questions, and the
	// counterexample details. The full review is on the result page.
	reviewFeedbackSummary reviewFeedbackDetail = "summary"
	// reviewFeedbackFull also prints the reviewer's findings verbatim.
	reviewFeedbackFull reviewFeedbackDetail = "full"
)

// machineReviewFeedback is the one setting that decides whether the
// reviewer's recommendations reach command output. This output is read by
// coding agents; with the summary an agent can tell the learner what the
// verdict is and where to read it, but it does not get the recommendations to
// apply on the learner's behalf.
const machineReviewFeedback = reviewFeedbackSummary

// evaluationFeedbackSource decodes only the parts of the public evaluation
// projection that any feedback detail level may print.
type evaluationFeedbackSource struct {
	Deterministic *struct {
		Status string `json:"status"`
		Checks []struct {
			ID             string          `json:"id"`
			Kind           string          `json:"kind"`
			Status         string          `json:"status"`
			Summary        string          `json:"summary"`
			Counterexample json.RawMessage `json:"counterexample"`
		} `json:"checks"`
	} `json:"deterministic"`
	Review *struct {
		Status        string  `json:"status"`
		Mode          string  `json:"mode"`
		Authoritative bool    `json:"authoritative"`
		Verdict       *string `json:"verdict"`
		Feedback      []struct {
			Priority    string `json:"priority"`
			Title       string `json:"title"`
			Observation string `json:"observation"`
			Risk        string `json:"risk"`
			Direction   string `json:"direction"`
		} `json:"feedback"`
		Rubric []struct {
			Criterion string `json:"criterion"`
			Level     int    `json:"level"`
		} `json:"rubric"`
		Uncertainty *struct {
			Blocking bool `json:"blocking"`
		} `json:"uncertainty"`
	} `json:"review"`
}

type machineFeedback struct {
	Detail        reviewFeedbackDetail      `json:"detail"`
	Deterministic *machineDeterministicPart `json:"deterministic,omitempty"`
	Review        *machineReviewPart        `json:"review,omitempty"`
}

type machineDeterministicPart struct {
	Status string                      `json:"status"`
	Checks []machineDeterministicCheck `json:"checks"`
}

type machineDeterministicCheck struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"`
	Status            string `json:"status"`
	Summary           string `json:"summary"`
	HasCounterexample bool   `json:"has_counterexample"`
}

type machineReviewPart struct {
	Status             string                 `json:"status"`
	Mode               string                 `json:"mode"`
	Authoritative      bool                   `json:"authoritative"`
	Verdict            *string                `json:"verdict"`
	Blocking           bool                   `json:"blocking_uncertainty"`
	FindingsCount      int                    `json:"findings_count"`
	FindingsByPriority map[string]int         `json:"findings_by_priority"`
	Rubric             []machineRubricLevel   `json:"rubric"`
	Findings           []machineReviewFinding `json:"findings,omitempty"`
}

type machineRubricLevel struct {
	Criterion string `json:"criterion"`
	Level     int    `json:"level"`
}

// machineReviewFinding is printed only at reviewFeedbackFull.
type machineReviewFinding struct {
	Priority    string `json:"priority"`
	Title       string `json:"title"`
	Observation string `json:"observation"`
	Risk        string `json:"risk"`
	Direction   string `json:"direction"`
}

// buildMachineFeedback projects a terminal evaluation at the given detail. A
// technical failure has neither part and yields an empty summary.
func buildMachineFeedback(raw json.RawMessage, detail reviewFeedbackDetail) (*machineFeedback, error) {
	var source evaluationFeedbackSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, fmt.Errorf("decode evaluation feedback: %w", err)
	}
	feedback := &machineFeedback{Detail: detail}
	if source.Deterministic != nil {
		part := &machineDeterministicPart{
			Status: source.Deterministic.Status,
			Checks: make([]machineDeterministicCheck, 0, len(source.Deterministic.Checks)),
		}
		for _, check := range source.Deterministic.Checks {
			part.Checks = append(part.Checks, machineDeterministicCheck{
				ID: check.ID, Kind: check.Kind, Status: check.Status, Summary: check.Summary,
				HasCounterexample: len(check.Counterexample) != 0 && string(check.Counterexample) != "null",
			})
		}
		feedback.Deterministic = part
	}
	if review := source.Review; review != nil {
		part := &machineReviewPart{
			Status: review.Status, Mode: review.Mode, Authoritative: review.Authoritative,
			Verdict: review.Verdict, FindingsCount: len(review.Feedback),
			FindingsByPriority: map[string]int{},
			Rubric:             make([]machineRubricLevel, 0, len(review.Rubric)),
		}
		if review.Uncertainty != nil {
			part.Blocking = review.Uncertainty.Blocking
		}
		for _, item := range review.Feedback {
			part.FindingsByPriority[item.Priority]++
			if detail == reviewFeedbackFull {
				part.Findings = append(part.Findings, machineReviewFinding{
					Priority: item.Priority, Title: item.Title, Observation: item.Observation,
					Risk: item.Risk, Direction: item.Direction,
				})
			}
		}
		for _, item := range review.Rubric {
			part.Rubric = append(part.Rubric, machineRubricLevel{Criterion: item.Criterion, Level: item.Level})
		}
		feedback.Review = part
	}
	return feedback, nil
}
