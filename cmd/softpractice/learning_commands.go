package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const defaultEvaluationTimeout = 10 * time.Minute

type machineEvaluationResult struct {
	Kind            string            `json:"kind"`
	Outcome         evaluationOutcome `json:"outcome"`
	SubmissionID    string            `json:"submission_id"`
	EvaluationJobID string            `json:"evaluation_job_id,omitempty"`
	JobState        string            `json:"job_state"`
	Attempt         int               `json:"attempt,omitempty"`
	MaxAttempts     int               `json:"max_attempts,omitempty"`
	NextPollSeconds int               `json:"next_poll_seconds,omitempty"`
	UpdatedAt       *time.Time        `json:"updated_at,omitempty"`
	ResultURL       string            `json:"result_url"`
	// Result is present only for the ready outcome.
	Result *machineResult `json:"result,omitempty"`
}

// buildMachineEvaluationResult projects one evaluation; resultURL is its
// result page.
func buildMachineEvaluationResult(
	result evaluationResult,
	resultURL string,
	detail reviewFeedbackDetail,
) (machineEvaluationResult, error) {
	payload := machineEvaluationResult{
		Kind: "softpractice.evaluation", Outcome: result.Outcome, SubmissionID: result.SubmissionID,
		ResultURL: resultURL,
	}
	if result.Outcome == evaluationSuperseded {
		payload.JobState = "superseded"
		return payload, nil
	}
	submission := result.Submission
	updatedAt := submission.UpdatedAt
	payload.EvaluationJobID = submission.EvaluationJobID
	payload.JobState = submission.JobState
	payload.Attempt = submission.Attempt
	payload.MaxAttempts = submission.MaxAttempts
	payload.NextPollSeconds = submission.NextPollSeconds
	payload.UpdatedAt = &updatedAt
	if result.Outcome == evaluationReady {
		built, err := buildMachineResult(result.RawEvaluation, detail)
		if err != nil {
			return machineEvaluationResult{}, err
		}
		payload.Result = built
	}
	return payload, nil
}

func resultCommand(
	ctx context.Context,
	useCases learnerUseCases,
	args []string,
	output, errorOutput io.Writer,
) (err error) {
	defer func() { err = reportJSONError(requestsJSON(args), output, err) }()
	flags := flag.NewFlagSet("result", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	id := flags.String("id", "", "submission ID; defaults to the latest submission")
	wait := flags.Bool("wait", false, "wait until the evaluation is ready")
	timeout := flags.Duration("timeout", defaultEvaluationTimeout, "maximum wait with --wait")
	directions := flags.Bool("directions", false, "also print the reviewer's directions and next steps")
	jsonOutput := jsonFlag(flags)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout < 0 {
		return usage(ctx,
			"использование: softpractice result [--id ID] [--wait] [--timeout DURATION] [--directions] [--json]",
			"usage: softpractice result [--id ID] [--wait] [--timeout DURATION] [--directions] [--json]")
	}
	submissionID := strings.TrimSpace(*id)
	if submissionID != "" && validateSubmissionID(submissionID) != nil {
		return usage(ctx, "ID отправки должен быть каноническим UUID", "submission ID must be a canonical UUID")
	}
	if submissionID == "" {
		latest, err := useCases.LatestSubmission(ctx)
		if err != nil {
			return err
		}
		if latest.PredecessorAssignmentID != "" {
			fmt.Fprintf(errorOutput, text(ctx,
				"По %s отправок ещё нет; показан принятый результат %s.\n",
				"%s has no submission yet; showing the accepted %s result.\n",
			), latest.CurrentAssignmentID, latest.PredecessorAssignmentID)
		}
		submissionID = latest.SubmissionID
	}
	return printEvaluation(ctx, useCases, submissionID, evaluationWait{Wait: *wait, Timeout: *timeout},
		*directions, *jsonOutput, output)
}

// printEvaluation reads or waits for one evaluation, prints it, and returns
// the exit status of a pending or superseded outcome.
func printEvaluation(
	ctx context.Context,
	useCases learnerUseCases,
	submissionID string,
	wait evaluationWait,
	directions, jsonOutput bool,
	output io.Writer,
) error {
	detail := defaultReviewFeedback
	if directions {
		detail = feedbackWithDirections
	}
	result, err := useCases.Evaluation(ctx, submissionID, wait)
	if err != nil {
		return err
	}
	payload, err := buildMachineEvaluationResult(result, webResultURL(ctx, submissionID), detail)
	if err != nil {
		return err
	}
	if jsonOutput {
		if err := writeMachineJSON(output, payload); err != nil {
			return err
		}
	} else {
		writeEvaluationMarkdown(ctx, output, payload)
	}
	switch payload.Outcome {
	case evaluationPending:
		return exitNotReady
	case evaluationSuperseded:
		return exitSuperseded
	}
	return nil
}

// writeEvaluationMarkdown prints the evaluation as Markdown, which reads as
// plain text in a terminal and renders in an agent's chat.
func writeEvaluationMarkdown(ctx context.Context, output io.Writer, payload machineEvaluationResult) {
	switch payload.Outcome {
	case evaluationSuperseded:
		fmt.Fprintf(output, text(ctx,
			"Проверка отправки `%s` заменена; результата у неё не будет.\n",
			"The evaluation of submission `%s` was superseded; it will not have a result.\n"),
			payload.SubmissionID)
		return
	case evaluationPending:
		fmt.Fprintf(output, text(ctx,
			"Проверка отправки `%s` ещё не готова (%s). Дождаться: `softpractice result --wait`\n",
			"The evaluation of submission `%s` is not ready yet (%s). To wait: `softpractice result --wait`\n"),
			payload.SubmissionID, payload.JobState)
		return
	}
	result := payload.Result
	fmt.Fprintf(output, "# %s\n\n", evaluationStatusTitle(ctx, result.Status))
	fmt.Fprintf(output, text(ctx, "Отправка `%s`, попытка проверки %d из %d.\n", "Submission `%s`, evaluation attempt %d of %d.\n"),
		payload.SubmissionID, payload.Attempt, payload.MaxAttempts)
	if technical := result.TechnicalError; technical != nil {
		fmt.Fprintf(output, text(ctx, "\nТехническая ошибка: `%s` на этапе `%s`, support ID `%s`.\n",
			"\nTechnical error: `%s` at stage `%s`, support ID `%s`.\n"),
			technical.ErrorCode, technical.Stage, technical.SupportID)
	}
	if deterministic := result.Deterministic; deterministic != nil {
		fmt.Fprintf(output, text(ctx, "\n## Автоматические проверки: %s\n\n", "\n## Automated checks: %s\n\n"),
			deterministicStatusLabel(ctx, deterministic.Status))
		for _, check := range deterministic.Checks {
			fmt.Fprintf(output, "- [%s] %s: %s\n", check.Status, check.ID, check.Summary)
			if example := check.Counterexample; example != nil {
				fmt.Fprintf(output, text(ctx, "  - Пример: %s\n", "  - Example: %s\n"), example.Title)
				fmt.Fprintf(output, text(ctx, "  - Сценарий: %s\n", "  - Scenario: %s\n"), example.Scenario)
				fmt.Fprintf(output, text(ctx, "  - Вход: %s\n", "  - Input: %s\n"), example.Input)
				fmt.Fprintf(output, text(ctx, "  - Ожидалось: %s\n", "  - Expected: %s\n"), example.Expected)
				if example.NextStep != "" {
					fmt.Fprintf(output, text(ctx, "  - Следующий шаг: %s\n", "  - Next step: %s\n"), example.NextStep)
				}
			}
		}
	}
	if review := result.Review; review != nil && review.Status != "skipped" {
		fmt.Fprint(output, text(ctx, "\n## Ревью\n", "\n## Review\n"))
		if !review.Authoritative {
			fmt.Fprint(output, text(ctx,
				"\nПредварительное ревью: оно не определяет результат.\n",
				"\nPreliminary review: it does not decide the result.\n"))
		}
		if review.Summary != "" {
			fmt.Fprintf(output, "\n%s\n", review.Summary)
		}
		if uncertainty := review.Uncertainty; uncertainty != nil && uncertainty.Reason != nil && *uncertainty.Reason != "" {
			fmt.Fprintf(output, text(ctx, "\nЧто осталось неясным: %s\n", "\nWhat remains unclear: %s\n"), *uncertainty.Reason)
		}
		if len(review.Rubric) > 0 {
			fmt.Fprint(output, text(ctx, "\n### Критерии\n\n", "\n### Criteria\n\n"))
			for _, item := range review.Rubric {
				fmt.Fprintf(output, "- **%s**: %s. %s%s\n", item.Criterion, rubricLevelLabel(ctx, item.Level),
					item.Rationale, formatEvidence(ctx, item.EvidenceRefs))
			}
		}
		if len(review.Findings) > 0 {
			fmt.Fprint(output, text(ctx, "\n### Замечания\n", "\n### Findings\n"))
			for index, finding := range review.Findings {
				fmt.Fprintf(output, "\n%d. **%s** (%s)\n", index+1, finding.Title, priorityLabel(ctx, finding.Priority))
				fmt.Fprintf(output, text(ctx, "   - Наблюдение: %s\n", "   - Observation: %s\n"), finding.Observation)
				fmt.Fprintf(output, text(ctx, "   - Риск: %s\n", "   - Risk: %s\n"), finding.Risk)
				if finding.Direction != "" {
					fmt.Fprintf(output, text(ctx, "   - Направление: %s\n", "   - Direction: %s\n"), finding.Direction)
				}
				if len(finding.EvidenceRefs) > 0 {
					fmt.Fprintf(output, text(ctx, "   - Где: %s\n", "   - Where: %s\n"), strings.Join(finding.EvidenceRefs, ", "))
				}
			}
		}
		switch {
		case review.QuestionsAnswerable:
			fmt.Fprintf(output, text(ctx,
				"\nРецензенту нужны ваши ответы на вопросы о решении (%d). Ответьте на странице результата: %s\n",
				"\nThe reviewer needs your answers to questions about the solution (%d). Answer them on the result page: %s\n"),
				review.QuestionsCount, payload.ResultURL)
		case review.QuestionsCount > 0:
			fmt.Fprintf(output, text(ctx,
				"\nВопросы рецензента для самопроверки (%d) — на странице результата.\n",
				"\nThe reviewer's self-check questions (%d) are on the result page.\n"),
				review.QuestionsCount)
		}
	}
	if result.DirectionsHidden {
		fmt.Fprint(output, text(ctx,
			"\nРекомендации рецензента не показаны. Показать: `softpractice result --directions`\n",
			"\nThe reviewer's recommendations are not shown. To show them: `softpractice result --directions`\n"))
	}
	fmt.Fprintf(output, text(ctx, "\nПолный разбор: %s\n", "\nFull review: %s\n"), payload.ResultURL)
}

func evaluationStatusTitle(ctx context.Context, status string) string {
	switch status {
	case "accepted":
		return text(ctx, "Решение принято", "Solution accepted")
	case "revise":
		return text(ctx, "Нужна доработка", "Revision needed")
	case "uncertain":
		return text(ctx, "Рецензенту нужно пояснение", "The reviewer needs an explanation")
	case "deterministic_failed":
		return text(ctx, "Автоматические проверки не пройдены", "Automated checks failed")
	case "ready_for_review":
		return text(ctx, "Автоматические проверки пройдены", "Automated checks passed")
	case "technical_failure":
		return text(ctx, "Проверка не завершилась из-за технической ошибки", "The evaluation failed for a technical reason")
	default:
		return status
	}
}

func deterministicStatusLabel(ctx context.Context, status string) string {
	switch status {
	case "passed":
		return text(ctx, "пройдены", "passed")
	case "failed":
		return text(ctx, "не пройдены", "failed")
	case "not_run":
		return text(ctx, "не запускались", "not run")
	case "technical_failure":
		return text(ctx, "техническая ошибка", "technical failure")
	default:
		return status
	}
}

// rubricLevelLabel uses the level names of the result page.
func rubricLevelLabel(ctx context.Context, level int) string {
	switch level {
	case 1:
		return text(ctx, "нужно доработать", "needs work")
	case 2:
		return text(ctx, "выполнено", "met")
	case 3:
		return text(ctx, "выполнено и закреплено", "met and reinforced")
	default:
		return text(ctx, "не подтверждено", "not confirmed")
	}
}

func priorityLabel(ctx context.Context, priority string) string {
	switch priority {
	case "high":
		return text(ctx, "высокий приоритет", "high priority")
	case "medium":
		return text(ctx, "средний приоритет", "medium priority")
	case "low":
		return text(ctx, "низкий приоритет", "low priority")
	default:
		return priority
	}
}

func formatEvidence(ctx context.Context, refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	return text(ctx, " Где: ", " Where: ") + strings.Join(refs, ", ")
}

// webLessonURL is the assignment page of a lesson; suffix selects a page
// under it, such as "/material".
func webLessonURL(ctx context.Context, assignmentID string, version int, suffix string) string {
	target := strings.TrimRight(settingsFromContext(ctx).WebURL, "/") + "/assignments/" + assignmentID + suffix
	if version > 0 {
		target += "?version=" + strconv.Itoa(version)
	}
	return target
}

func webPracticumCompletionURL(ctx context.Context, practicumID string) string {
	return strings.TrimRight(settingsFromContext(ctx).WebURL, "/") + "/practicums/" + practicumID + "/completion"
}

func webResultURL(ctx context.Context, submissionID string) string {
	return strings.TrimRight(settingsFromContext(ctx).WebURL, "/") + "/submissions/" + submissionID + "/result"
}

type machineTask struct {
	Kind                string                      `json:"kind"`
	WorkspaceID         string                      `json:"workspace_id"`
	ProjectID           string                      `json:"project_id"`
	Assignment          assignmentDetail            `json:"assignment"`
	URL                 string                      `json:"url"`
	Transition          *machineTransition          `json:"transition,omitempty"`
	LessonVersionUpdate *machineLessonVersionUpdate `json:"lesson_version_update,omitempty"`
}

func taskCommand(ctx context.Context, useCases learnerUseCases, args []string, output, errorOutput io.Writer) (err error) {
	defer func() { err = reportJSONError(requestsJSON(args), output, err) }()
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	jsonOutput := jsonFlag(flags)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usage(ctx, "использование: softpractice task [--json]", "usage: softpractice task [--json]")
	}
	lesson, assignment, err := useCases.Task(ctx)
	if err != nil {
		return err
	}
	payload := machineTask{
		Kind: "softpractice.task", WorkspaceID: lesson.Workspace.Workspace.ID, ProjectID: lesson.Link.ProjectID,
		Assignment: assignment, URL: webLessonURL(ctx, assignment.ID, assignment.Version, ""),
	}
	if pendingTransition(lesson.Link, lesson.Workspace) {
		payload.Transition = &machineTransition{
			FromAssignmentID: lesson.Link.AssignmentID, FromAssignmentVersion: lesson.Link.AssignmentVersion,
			ToAssignmentID: lesson.Workspace.Assignment.ID, ToAssignmentVersion: lesson.Workspace.Assignment.Version,
		}
	}
	if pendingLessonVersion(lesson.Link, lesson.Workspace) {
		payload.LessonVersionUpdate = &machineLessonVersionUpdate{
			AssignmentID: lesson.Link.AssignmentID,
			FromVersion:  lesson.Link.AssignmentVersion, ToVersion: lesson.Workspace.Assignment.Version,
		}
	}
	if *jsonOutput {
		return writeMachineJSON(output, payload)
	}
	fmt.Fprintf(output, "# %s\n\n", assignment.Title)
	fmt.Fprintf(output, text(ctx, "Урок `%s` v%d, около %d мин.\n\n", "Lesson `%s` v%d, about %d min.\n\n"),
		assignment.ID, assignment.Version, assignment.EstimatedMinutes)
	if payload.Transition != nil {
		fmt.Fprintf(output, text(ctx,
			"Файлов урока `%s` ещё нет в этой папке: выполните `softpractice update`.\n\n",
			"The `%s` lesson files are not in this folder yet: run `softpractice update`.\n\n"),
			payload.Transition.ToAssignmentID)
	}
	if payload.LessonVersionUpdate != nil {
		fmt.Fprintf(output, text(ctx,
			"Опубликована версия v%d этого урока; перейти на неё: `softpractice update`.\n\n",
			"Version v%d of this lesson is published; to move to it: `softpractice update`.\n\n"),
			payload.LessonVersionUpdate.ToVersion)
	}
	fmt.Fprintln(output, strings.TrimRight(assignment.InstructionsMarkdown, "\n"))
	fmt.Fprintf(output, text(ctx, "\nМатериал: `softpractice material`. Страница задания: %s\n",
		"\nMaterial: `softpractice material`. Assignment page: %s\n"), payload.URL)
	return nil
}

type machineMaterial struct {
	Kind         string          `json:"kind"`
	AssignmentID string          `json:"assignment_id"`
	Version      int             `json:"version,omitempty"`
	Material     *theoryMaterial `json:"material"`
	URL          string          `json:"url"`
}

func materialCommand(ctx context.Context, useCases learnerUseCases, args []string, output, errorOutput io.Writer) (err error) {
	defer func() { err = reportJSONError(requestsJSON(args), output, err) }()
	flags := flag.NewFlagSet("material", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	lessonID := flags.String("lesson", "", "lesson ID; defaults to the lesson of this project")
	jsonOutput := jsonFlag(flags)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usage(ctx, "использование: softpractice material [--lesson ID] [--json]",
			"usage: softpractice material [--lesson ID] [--json]")
	}
	snapshot, err := useCases.Material(ctx, strings.TrimSpace(*lessonID))
	if err != nil {
		return err
	}
	payload := machineMaterial{
		Kind: "softpractice.material", AssignmentID: snapshot.AssignmentID, Version: snapshot.Version,
		Material: snapshot.Material, URL: webLessonURL(ctx, snapshot.AssignmentID, snapshot.Version, "/material"),
	}
	if *jsonOutput {
		return writeMachineJSON(output, payload)
	}
	material := snapshot.Material
	if material == nil {
		fmt.Fprintf(output, text(ctx, "У урока `%s` нет теоретического материала.\n",
			"Lesson `%s` has no theory material.\n"), snapshot.AssignmentID)
		return nil
	}
	fmt.Fprintln(output, strings.TrimRight(material.Markdown, "\n"))
	if diagram := material.Diagram; diagram.Title != "" {
		fmt.Fprintf(output, text(ctx, "\n## Схема: %s\n\n", "\n## Diagram: %s\n\n"), diagram.Title)
		for _, node := range diagram.Nodes {
			fmt.Fprintf(output, "- **%s**: %s\n", node.Title, node.Text)
		}
		if diagram.TextAlternative != "" {
			fmt.Fprintf(output, "\n%s\n", diagram.TextAlternative)
		}
	}
	fmt.Fprintf(output, text(ctx, "\nМатериал на сайте: %s\n", "\nMaterial on the site: %s\n"), payload.URL)
	return nil
}

type machineHints struct {
	Kind         string         `json:"kind"`
	AssignmentID string         `json:"assignment_id"`
	Version      int            `json:"version"`
	Status       string         `json:"status"`
	Revealed     []preparedHint `json:"revealed"`
	// URL is the assignment page, where the learner opens the next hint.
	URL string `json:"url"`
}

func hintCommand(ctx context.Context, useCases learnerUseCases, args []string, output, errorOutput io.Writer) (err error) {
	defer func() { err = reportJSONError(requestsJSON(args), output, err) }()
	flags := flag.NewFlagSet("hint", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	jsonOutput := jsonFlag(flags)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usage(ctx, "использование: softpractice hint [--json]", "usage: softpractice hint [--json]")
	}
	snapshot, err := useCases.Hints(ctx)
	if err != nil {
		return err
	}
	payload := machineHints{
		Kind: "softpractice.hints", AssignmentID: snapshot.Lesson.AssignmentID, Version: snapshot.Lesson.Version,
		Status: snapshot.Status, Revealed: snapshot.Revealed,
		URL: webLessonURL(ctx, snapshot.Lesson.AssignmentID, snapshot.Lesson.Version, ""),
	}
	if *jsonOutput {
		return writeMachineJSON(output, payload)
	}
	if snapshot.Status == "unavailable" {
		fmt.Fprintf(output, text(ctx, "У урока `%s` нет подготовленных подсказок.\n",
			"Lesson `%s` has no prepared hints.\n"), snapshot.Lesson.AssignmentID)
		return nil
	}
	for _, hint := range snapshot.Revealed {
		fmt.Fprintf(output, text(ctx, "## Подсказка %d\n\n%s\n\n", "## Hint %d\n\n%s\n\n"),
			hint.Order, strings.TrimRight(hint.Markdown, "\n"))
	}
	switch {
	case snapshot.Status == "available" && len(snapshot.Revealed) == 0:
		fmt.Fprintf(output, text(ctx, "Открытых подсказок пока нет. Открыть подсказку можно на странице задания: %s\n",
			"No hints are open yet. You can open a hint on the assignment page: %s\n"), payload.URL)
	case snapshot.Status == "available":
		fmt.Fprintf(output, text(ctx, "Следующую подсказку можно открыть на странице задания: %s\n",
			"You can open the next hint on the assignment page: %s\n"), payload.URL)
	default:
		fmt.Fprintln(output, text(ctx, "Все подсказки урока открыты.", "All hints of this lesson are open."))
	}
	return nil
}

type machineSubmissions struct {
	Kind         string              `json:"kind"`
	AssignmentID string              `json:"assignment_id"`
	Version      int                 `json:"version"`
	History      []submissionAttempt `json:"history"`
}

func submissionsCommand(
	ctx context.Context,
	useCases learnerUseCases,
	args []string,
	output, errorOutput io.Writer,
) (err error) {
	defer func() { err = reportJSONError(requestsJSON(args), output, err) }()
	if len(args) > 0 && args[0] == "download" {
		return downloadSubmissionRevision(ctx, useCases.client, args[1:], output, errorOutput)
	}
	flags := flag.NewFlagSet("submissions", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	jsonOutput := jsonFlag(flags)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usage(ctx,
			"использование: softpractice submissions [--json]\n              softpractice submissions download --id ID [--output PATH]",
			"usage: softpractice submissions [--json]\n       softpractice submissions download --id ID [--output PATH]")
	}
	snapshot, err := useCases.Submissions(ctx)
	if err != nil {
		return err
	}
	payload := machineSubmissions{
		Kind: "softpractice.submissions", AssignmentID: snapshot.Lesson.AssignmentID,
		Version: snapshot.Lesson.Version, History: snapshot.History,
	}
	if *jsonOutput {
		return writeMachineJSON(output, payload)
	}
	if len(snapshot.History) == 0 {
		fmt.Fprintf(output, text(ctx, "По уроку `%s` отправок пока нет.\n", "Lesson `%s` has no submissions yet.\n"),
			snapshot.Lesson.AssignmentID)
		return nil
	}
	fmt.Fprintf(output, text(ctx, "Отправки урока `%s`, сначала новые:\n\n", "Submissions of lesson `%s`, newest first:\n\n"),
		snapshot.Lesson.AssignmentID)
	for _, attempt := range snapshot.History {
		status := attempt.JobState
		if attempt.EvaluationStatus != nil {
			status = evaluationStatusTitle(ctx, *attempt.EvaluationStatus)
		}
		fmt.Fprintf(output, text(ctx, "%d. %s — %s — `%s`\n", "%d. %s — %s — `%s`\n"),
			attempt.LearningAttempt, attempt.SubmittedAt.Local().Format("2006-01-02 15:04"), status, attempt.SubmissionID)
	}
	fmt.Fprintln(output, text(ctx, "\nРезультат отправки: `softpractice result --id ID`",
		"\nResult of a submission: `softpractice result --id ID`"))
	return nil
}
