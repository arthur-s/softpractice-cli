package main

import (
	"encoding/json"
	"fmt"
)

// reviewFeedbackDetail says how much of an evaluation the `evaluation` command
// prints, in both text and JSON.
type reviewFeedbackDetail string

const (
	// feedbackWithoutDirections prints what the checks and the reviewer found
	// and why it matters: check results with their counterexamples, the
	// review summary, the rubric with rationales, and each finding's title,
	// observation, and risk. It leaves out what to do about it: the findings'
	// directions and the counterexamples' next steps.
	feedbackWithoutDirections reviewFeedbackDetail = "without_directions"
	// feedbackWithDirections also prints the directions and next steps.
	feedbackWithDirections reviewFeedbackDetail = "with_directions"
)

// defaultReviewFeedback is the one setting that decides whether the
// reviewer's recommendations reach command output when the learner did not
// ask for them with --directions. This output is also read by coding agents:
// by default an agent can show the learner the review, but it does not get a
// list of fixes to apply on the learner's behalf.
//
// The reviewer's questions to the learner are never printed at any detail;
// only their number is. They are answered by the learner, not by an agent
// that happens to read the output.
const defaultReviewFeedback = feedbackWithoutDirections

// evaluationSource decodes the public evaluation projection that the
// `evaluation` command renders.
type evaluationSource struct {
	ExerciseID     string `json:"exercise_id"`
	Status         string `json:"status"`
	TechnicalError *struct {
		ErrorCode string `json:"error_code"`
		Stage     string `json:"stage"`
		SupportID string `json:"support_id"`
	} `json:"technical_error"`
	Deterministic *struct {
		Status string `json:"status"`
		Checks []struct {
			ID             string `json:"id"`
			Kind           string `json:"kind"`
			Status         string `json:"status"`
			Summary        string `json:"summary"`
			Counterexample *struct {
				Title    string `json:"title"`
				Scenario string `json:"scenario"`
				Input    string `json:"input"`
				Expected string `json:"expected"`
				NextStep string `json:"next_step"`
			} `json:"counterexample"`
		} `json:"checks"`
	} `json:"deterministic"`
	Review *struct {
		Status        string   `json:"status"`
		Mode          string   `json:"mode"`
		Authoritative bool     `json:"authoritative"`
		Verdict       *string  `json:"verdict"`
		Summary       string   `json:"summary"`
		Questions     []string `json:"defense_questions"`
		Uncertainty   *struct {
			Blocking bool    `json:"blocking"`
			Reason   *string `json:"reason"`
		} `json:"uncertainty"`
		Rubric []struct {
			Criterion    string   `json:"criterion"`
			Level        int      `json:"level"`
			Rationale    string   `json:"rationale"`
			EvidenceRefs []string `json:"evidence_refs"`
		} `json:"rubric"`
		Feedback []struct {
			Priority     string   `json:"priority"`
			Title        string   `json:"title"`
			Observation  string   `json:"observation"`
			Risk         string   `json:"risk"`
			Direction    string   `json:"direction"`
			EvidenceRefs []string `json:"evidence_refs"`
		} `json:"feedback"`
	} `json:"review"`
}

type machineResult struct {
	Status     string               `json:"status"`
	ExerciseID string               `json:"exercise_id"`
	Detail     reviewFeedbackDetail `json:"detail"`
	// DirectionsHidden is true when directions or next steps exist but this
	// output leaves them out; --directions prints them.
	DirectionsHidden bool                   `json:"directions_hidden"`
	TechnicalError   *machineTechnicalError `json:"technical_error,omitempty"`
	Deterministic    *machineDeterministic  `json:"deterministic,omitempty"`
	Review           *machineReviewResult   `json:"review,omitempty"`
}

type machineDeterministic struct {
	Status string                      `json:"status"`
	Checks []machineDeterministicCheck `json:"checks"`
}

type machineDeterministicCheck struct {
	ID             string                 `json:"id"`
	Kind           string                 `json:"kind"`
	Status         string                 `json:"status"`
	Summary        string                 `json:"summary"`
	Counterexample *machineCounterexample `json:"counterexample,omitempty"`
}

type machineCounterexample struct {
	Title    string `json:"title"`
	Scenario string `json:"scenario"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
	// NextStep is printed only with directions.
	NextStep string `json:"next_step,omitempty"`
}

type machineReviewResult struct {
	Status        string                 `json:"status"`
	Mode          string                 `json:"mode"`
	Authoritative bool                   `json:"authoritative"`
	Verdict       *string                `json:"verdict"`
	Summary       string                 `json:"summary,omitempty"`
	Uncertainty   *machineUncertainty    `json:"uncertainty,omitempty"`
	Rubric        []machineRubricItem    `json:"rubric"`
	Findings      []machineReviewFinding `json:"findings"`
	// QuestionsCount is how many questions the reviewer asked the learner.
	// Their text is on the result page.
	QuestionsCount int `json:"questions_count"`
}

type machineUncertainty struct {
	Blocking bool    `json:"blocking"`
	Reason   *string `json:"reason"`
}

type machineRubricItem struct {
	Criterion    string   `json:"criterion"`
	Level        int      `json:"level"`
	Rationale    string   `json:"rationale"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type machineReviewFinding struct {
	Priority     string   `json:"priority"`
	Title        string   `json:"title"`
	Observation  string   `json:"observation"`
	Risk         string   `json:"risk"`
	Direction    string   `json:"direction,omitempty"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// buildMachineResult projects a terminal evaluation at the given detail.
func buildMachineResult(raw json.RawMessage, detail reviewFeedbackDetail) (*machineResult, error) {
	var source evaluationSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, fmt.Errorf("decode evaluation: %w", err)
	}
	withDirections := detail == feedbackWithDirections
	directionsHidden := false
	result := &machineResult{Status: source.Status, ExerciseID: source.ExerciseID, Detail: detail}
	if technical := source.TechnicalError; technical != nil {
		result.TechnicalError = &machineTechnicalError{
			ErrorCode: technical.ErrorCode, Stage: technical.Stage, SupportID: technical.SupportID,
		}
	}
	if deterministic := source.Deterministic; deterministic != nil {
		part := &machineDeterministic{
			Status: deterministic.Status,
			Checks: make([]machineDeterministicCheck, 0, len(deterministic.Checks)),
		}
		for _, check := range deterministic.Checks {
			item := machineDeterministicCheck{ID: check.ID, Kind: check.Kind, Status: check.Status, Summary: check.Summary}
			if example := check.Counterexample; example != nil {
				item.Counterexample = &machineCounterexample{
					Title: example.Title, Scenario: example.Scenario,
					Input: example.Input, Expected: example.Expected,
				}
				if withDirections {
					item.Counterexample.NextStep = example.NextStep
				} else if example.NextStep != "" {
					directionsHidden = true
				}
			}
			part.Checks = append(part.Checks, item)
		}
		result.Deterministic = part
	}
	if review := source.Review; review != nil {
		part := &machineReviewResult{
			Status: review.Status, Mode: review.Mode, Authoritative: review.Authoritative,
			Verdict: review.Verdict, Summary: review.Summary,
			Rubric:         make([]machineRubricItem, 0, len(review.Rubric)),
			Findings:       make([]machineReviewFinding, 0, len(review.Feedback)),
			QuestionsCount: len(review.Questions),
		}
		if uncertainty := review.Uncertainty; uncertainty != nil {
			part.Uncertainty = &machineUncertainty{Blocking: uncertainty.Blocking, Reason: uncertainty.Reason}
		}
		for _, item := range review.Rubric {
			part.Rubric = append(part.Rubric, machineRubricItem{
				Criterion: item.Criterion, Level: item.Level,
				Rationale: item.Rationale, EvidenceRefs: nonNilStrings(item.EvidenceRefs),
			})
		}
		for _, item := range review.Feedback {
			finding := machineReviewFinding{
				Priority: item.Priority, Title: item.Title, Observation: item.Observation,
				Risk: item.Risk, EvidenceRefs: nonNilStrings(item.EvidenceRefs),
			}
			if withDirections {
				finding.Direction = item.Direction
			} else if item.Direction != "" {
				directionsHidden = true
			}
			part.Findings = append(part.Findings, finding)
		}
		result.Review = part
	}
	result.DirectionsHidden = directionsHidden
	return result, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
