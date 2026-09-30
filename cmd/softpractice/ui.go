package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type language string

const (
	languageRussian language = "ru"
	languageEnglish language = "en"
)

type runtimeSettings struct {
	APIURL   string
	WebURL   string
	Language language
	Config   configStore
}

type settingsContextKey struct{}

func settingsFromContext(ctx context.Context) runtimeSettings {
	settings, _ := ctx.Value(settingsContextKey{}).(runtimeSettings)
	if settings.Language != languageRussian && settings.Language != languageEnglish {
		settings.Language = languageEnglish // Keeps direct command tests deterministic.
	}
	if settings.WebURL == "" {
		settings.WebURL = "https://localhost:5173"
	}
	return settings
}

func withSettings(ctx context.Context, settings runtimeSettings) context.Context {
	return context.WithValue(ctx, settingsContextKey{}, settings)
}

func resolveSettings(store configStore) (runtimeSettings, error) {
	config, err := store.Load()
	if err != nil {
		return runtimeSettings{}, err
	}
	settings := defaultSettings(store)
	if config.APIURL != "" {
		settings.APIURL = config.APIURL
	}
	if config.WebURL != "" {
		settings.WebURL = config.WebURL
	}
	if config.Language != "" {
		settings.Language = language(config.Language)
	}
	if value := strings.TrimSpace(os.Getenv("SOFTPRACTICE_API_URL")); value != "" {
		settings.APIURL = value
	}
	if value := strings.TrimSpace(os.Getenv("SOFTPRACTICE_WEB_URL")); value != "" {
		settings.WebURL = value
	}
	if value := strings.TrimSpace(os.Getenv("SOFTPRACTICE_LANGUAGE")); value != "" {
		resolved, err := parseLanguage(value)
		if err != nil {
			return runtimeSettings{}, err
		}
		settings.Language = resolved
	}
	return settings, nil
}

// displaySettings is deliberately best-effort. Help and `set` must remain
// usable when the configuration is malformed, but should honour a valid saved
// language whenever one is available.
func displaySettings(store configStore) runtimeSettings {
	settings := defaultSettings(store)
	if config, err := store.Load(); err == nil {
		if value := language(config.Language); validateRuntimeLanguage(value) == nil {
			settings.Language = value
		}
	}
	if value, err := parseLanguage(os.Getenv("SOFTPRACTICE_LANGUAGE")); err == nil && strings.TrimSpace(os.Getenv("SOFTPRACTICE_LANGUAGE")) != "" {
		settings.Language = value
	}
	return settings
}

func settingOverrideNotice(ctx context.Context, setting, saved string) string {
	variable := environmentVariableForSetting(setting)
	if variable == "" || strings.TrimSpace(saved) == "" {
		return ""
	}
	return text(ctx,
		" (сохранено: "+saved+", перекрыто "+variable+")",
		" (saved: "+saved+", overridden by "+variable+")")
}

func validateRuntimeLanguage(value language) error {
	if value != languageRussian && value != languageEnglish {
		return errors.New("language must be ru or en")
	}
	return nil
}

func defaultSettings(store configStore) runtimeSettings {
	return runtimeSettings{
		APIURL: "http://localhost:8080", WebURL: "https://localhost:5173",
		Language: detectSystemLanguage(), Config: store,
	}
}

// languageChosen reports whether the learner chose a language anywhere the
// CLI looks for one: the saved setting, SOFTPRACTICE_LANGUAGE, or a Russian
// or English system locale. The --lang flag is checked by the caller.
func languageChosen(store configStore) bool {
	if config, err := store.Load(); err == nil && config.Language != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("SOFTPRACTICE_LANGUAGE")) != "" {
		return true
	}
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if languageFromLocale(os.Getenv(name)) != "" {
			return true
		}
	}
	return languageFromLocale(systemLocaleName()) != ""
}

func detectSystemLanguage() language {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if detected := languageFromLocale(os.Getenv(name)); detected != "" {
			return detected
		}
	}
	if detected := languageFromLocale(systemLocaleName()); detected != "" {
		return detected
	}
	return languageEnglish
}

func languageFromLocale(value string) language {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "en") {
		return languageEnglish
	}
	if strings.HasPrefix(value, "ru") {
		return languageRussian
	}
	return ""
}

func parseLanguage(value string) (language, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ru", "russian":
		return languageRussian, nil
	case "en", "english":
		return languageEnglish, nil
	default:
		return "", errors.New("language must be ru or en")
	}
}

func text(ctx context.Context, russian, english string) string {
	if settingsFromContext(ctx).Language == languageEnglish {
		return english
	}
	return russian
}

func printHelp(ctx context.Context, output io.Writer) {
	fmt.Fprintln(output, text(ctx,
		`Softpractice — тренажёр инженерной практики

Использование:
  softpractice [--api URL] [--lang ru|en] <команда>

Начните здесь:
  login                 Войти через браузер
  starter               Скачать проект первого урока
  status                Показать состояние проекта и следующий шаг

Урок:
  task                  Показать задание
  material              Показать теоретический материал
  hint                  Показать открытые подсказки

Решение:
  check                 Проверить текущие изменения
  submit                Отправить зафиксированный Git-коммит
  result                Показать результат проверки
  submissions           Показать отправки урока
  update                Применить переход к следующему уроку
  open                  Открыть задание, материал или результат в браузере
  mcp                   MCP-сервер для Claude Code, Codex и Claude Desktop

Настройки:
  set api-url URL       Сохранить адрес API
  set web-url URL       Сохранить адрес web-приложения
  set lang ru|en        Сохранить язык CLI
  set auto-checks true|false  Автозапуск проверок при отправке
  config                Показать действующие настройки

Прочее:
  logout                Завершить CLI-сессию
  project restore       Восстановить продолжимый связанный проект

Версия:
  version               Показать версию CLI

Команды status, task, material, hint, result, submissions и submit принимают --json.
Коды выхода: 0 — готово, 1 — ошибка, 2 — неверные аргументы, 3 — проверка ещё не готова, 4 — проверка заменена.

Подробнее:
  softpractice help <команда>`,
		`Softpractice — engineering practice simulator

Usage:
  softpractice [--api URL] [--lang ru|en] <command>

Get started:
  login                 Sign in in a browser
  starter               Download the first lesson project
  status                Show the project state and the next step

Lesson:
  task                  Show the assignment
  material              Show the theory material
  hint                  Show opened hints

Solution:
  check                 Check the current changes
  submit                Submit the committed Git revision
  result                Show the evaluation result
  submissions           Show the lesson's submissions
  update                Apply the next lesson transition
  open                  Open the task, material, or result in a browser
  mcp                   MCP server for Claude Code, Codex, and Claude Desktop

Settings:
  set api-url URL       Save the API address
  set web-url URL       Save the web app address
  set lang ru|en        Save the CLI language
  set auto-checks true|false  Run checks automatically on submit
  config                Show effective settings

Other:
  logout                Revoke the CLI session
  project restore       Restore a resumable linked project

Version:
  version               Show the CLI version

status, task, material, hint, result, submissions, and submit accept --json.
Exit codes: 0 done, 1 error, 2 invalid arguments, 3 evaluation not ready yet, 4 evaluation superseded.

More help:
  softpractice help <command>`))
}

func printCommandHelp(ctx context.Context, output io.Writer, command string) error {
	var russian, english string
	switch command {
	case "login":
		russian, english = "Использование: softpractice login [--no-browser]\n\nЗапускает вход через браузер и сохраняет refresh-токен в системном keyring.", "Usage: softpractice login [--no-browser]\n\nStarts browser sign-in and saves the refresh token in the system keyring."
	case "logout":
		russian, english = "Использование: softpractice logout\n\nОтзывает refresh-токен и удаляет локальные данные CLI-сессии.", "Usage: softpractice logout\n\nRevokes the refresh token and deletes local CLI session data."
	case "starter":
		russian, english = "Использование: softpractice starter [--practicum ID] [--directory PATH]\n\nСкачивает starter текущего урока в новую папку.", "Usage: softpractice starter [--practicum ID] [--directory PATH]\n\nDownloads the current lesson starter into a new directory."
	case "project":
		russian, english = "Использование: softpractice project restore [--practicum ID] [--directory PATH]\n\nВосстанавливает последнюю отправленную ревизию в новый связанный Git-проект. Если текущий урок ещё не имеет отправок, восстанавливает принятую предыдущую ревизию и применяет авторизованный update. Без истории отправок использует starter текущего урока.", "Usage: softpractice project restore [--practicum ID] [--directory PATH]\n\nRestores the latest submitted revision into a new linked Git project. If the current lesson has no submissions, it restores the accepted predecessor and applies the authorized update. With no submission history it uses the current lesson starter."
	case "status":
		russian, english = "Использование: softpractice status [--json]\n\nПоказывает состояние связанного проекта, последнюю отправку и следующий шаг. В JSON следующие шаги — в next_actions.", "Usage: softpractice status [--json]\n\nShows the linked project, its latest submission, and the next step. In JSON the next steps are in next_actions."
	case "submissions":
		russian, english = "Использование: softpractice submissions [--json]\n              softpractice submissions download --id ID [--output PATH]\n\nПоказывает последние отправки текущего урока с результатами. download сохраняет ZIP точной отправленной ревизии, как кнопка «Скачать проект» на странице результата.", "Usage: softpractice submissions [--json]\n       softpractice submissions download --id ID [--output PATH]\n\nShows the recent submissions of the current lesson with their results. download saves the exact submitted revision ZIP, equivalent to the result-page Download project button."
	case "result":
		russian, english = "Использование: softpractice result [--id ID] [--wait] [--timeout DURATION] [--directions] [--json]\n\nПоказывает результат проверки последней отправки или отправки --id в формате Markdown. С --wait ждёт завершения проверки, опрашивая сервер с назначенным им интервалом, не дольше --timeout (по умолчанию 10m). Рекомендации рецензента и следующие шаги выводятся только с --directions. На вопросы рецензента отвечают на странице результата.\n\nКоды выхода: 0 — результат готов, 1 — ошибка, 3 — проверка ещё не готова, 4 — проверка заменена и результата не будет.", "Usage: softpractice result [--id ID] [--wait] [--timeout DURATION] [--directions] [--json]\n\nShows the evaluation result of the latest submission or of --id as Markdown. With --wait it waits for the evaluation, polling at the interval the server sets, for at most --timeout (default 10m). The reviewer's recommendations and next steps are printed only with --directions. The reviewer's questions are answered on the result page.\n\nExit codes: 0 result ready, 1 error, 3 not ready yet, 4 superseded, no result."
	case "task":
		russian, english = "Использование: softpractice task [--json]\n\nПоказывает задание той версии урока, над которой работает этот проект.", "Usage: softpractice task [--json]\n\nShows the assignment of the lesson version this project is working on."
	case "material":
		russian, english = "Использование: softpractice material [--lesson ID] [--json]\n\nПоказывает теоретический материал урока этого проекта или урока --lesson.", "Usage: softpractice material [--lesson ID] [--json]\n\nShows the theory material of this project's lesson or of --lesson."
	case "hint":
		russian, english = "Использование: softpractice hint [--json]\n\nПоказывает подсказки урока, которые вы уже открыли. Следующая подсказка открывается на странице задания.", "Usage: softpractice hint [--json]\n\nShows the lesson hints you have already opened. The next hint is opened on the assignment page."
	case "submit":
		russian, english = "Использование: softpractice submit [--yes] [--checks=true|false] [--wait] [--timeout DURATION] [--directions] [--json]\n\nОтправляет чистый Git commit текущего урока на проверку. С --wait затем ждёт результат, как softpractice result --wait; --directions действует так же, как у result. Отказ в подтверждении завершает команду с кодом 1: ничего не отправлено.", "Usage: softpractice submit [--yes] [--checks=true|false] [--wait] [--timeout DURATION] [--directions] [--json]\n\nSubmits the clean Git commit for the current lesson. With --wait it then waits for the result, like softpractice result --wait; --directions works as it does for result. Declining the confirmation exits with code 1: nothing was submitted."
	case "check":
		russian, english = "Использование: softpractice check\n\nЗапускает публичные проверки для текущего рабочего дерева, включая незакоммиченные изменения. Ничего не отправляет на сервер.", "Usage: softpractice check\n\nRuns public checks against the current working tree, including uncommitted changes. Does not submit anything to the server."
	case "update":
		russian, english = "Использование: softpractice update\n\nПосле принятия решения применяет в этом же проекте переход к следующему уроку: добавляет, заменяет или удаляет только явно объявленные файлы.\n\nПереход применяется к принятому решению прошлого урока. Если текущий коммит — другой, команда ничего не меняет и показывает оба коммита и способ продолжить.", "Usage: softpractice update\n\nAfter acceptance, applies the next-lesson transition in the same project, adding, replacing, or removing only explicitly declared files.\n\nThe transition applies to the accepted solution of the previous lesson. When the current commit is a different one, the command changes nothing and shows both commits and how to continue."
	case "mcp":
		russian, english = "Использование: softpractice mcp [--project DIR]\n\nЗапускает локальный MCP-сервер на stdio для Claude Code, Codex, Claude Desktop и других агентов. Инструменты: status, task, material, hints, check, submit, result, submissions, update. submit и update просят подтверждения человека. Рекомендации рецензента и текст его вопросов сервер не отдаёт. Сначала войдите: softpractice login. --project задаёт папку проекта, если клиент запускает сервер не в ней. Язык сообщений: --lang, SOFTPRACTICE_LANGUAGE или set lang; по умолчанию русский.", "Usage: softpractice mcp [--project DIR]\n\nStarts a local stdio MCP server for Claude Code, Codex, Claude Desktop, and other agents. Tools: status, task, material, hints, check, submit, result, submissions, update. submit and update ask a person to confirm. The server never returns the reviewer's directions or the text of the reviewer's questions. Sign in first: softpractice login. --project sets the project folder when the client starts the server elsewhere. Message language: --lang, SOFTPRACTICE_LANGUAGE, or set lang; Russian by default."
	case "open":
		russian, english = "Использование: softpractice open [task|material|result] [--web URL] [--no-browser]\n\nОткрывает в браузере задание, материал или последний результат. Без аргумента — результат, а до первой отправки — задание.", "Usage: softpractice open [task|material|result] [--web URL] [--no-browser]\n\nOpens the task, the material, or the latest result in a browser. Without an argument: the result, or the task before the first submission."
	case "set":
		russian, english = "Использование: softpractice set api-url|web-url|lang|auto-checks VALUE\n\nСохраняет пользовательскую настройку CLI. auto-checks true|false действует только в текущем проекте.", "Usage: softpractice set api-url|web-url|lang|auto-checks VALUE\n\nSaves a user-level CLI setting. auto-checks true|false applies only to the current project."
	case "config":
		russian, english = "Использование: softpractice config\n\nПоказывает действующие настройки и путь к конфигурации.", "Usage: softpractice config\n\nShows effective settings and the configuration path."
	case "version":
		russian, english = "Использование: softpractice version, или softpractice --version\n\nПоказывает версию CLI. Сборка из исходников без версии отправку решения не пройдёт: сервер принимает только семантическую версию.", "Usage: softpractice version, or softpractice --version\n\nShows the CLI version. A source build without one cannot submit: the server accepts only a semantic version."
	default:
		return usageError{message: fmt.Sprintf(text(ctx, "неизвестная команда %q", "unknown command %q"), command)}
	}
	fmt.Fprintln(output, text(ctx, russian, english))
	return nil
}
