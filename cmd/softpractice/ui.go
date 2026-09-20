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
		"Softpractice — тренажёр инженерной практики\n\nИспользование:\n  softpractice [--api URL] [--lang ru|en] <команда>\n\nНачните здесь:\n  login                 Войти через браузер\n  logout                Завершить CLI-сессию\n  starter               Скачать проект первого урока\n  status                Показать текущий урок и состояние проекта\n\nРабота в проекте:\n  submit                Отправить зафиксированный Git commit\n  submission show       Показать состояние и результат отправки\n  submission download   Скачать ZIP сохранённой ревизии\n  update                Применить переход к следующему доступному уроку\n  open                  Открыть workspace или результат в браузере\n\nНастройки:\n  set api-url URL       Сохранить адрес API\n  set web-url URL       Сохранить адрес web-приложения\n  set lang ru|en        Сохранить язык CLI\n  set auto-checks true|false  Автозапуск тестов в этом проекте\n  config                Показать действующие настройки\n\nПодробнее:\n  softpractice help <команда>",
		"Softpractice — engineering practice simulator\n\nUsage:\n  softpractice [--api URL] [--lang ru|en] <command>\n\nGet started:\n  login                 Sign in in a browser\n  logout                Revoke the CLI session\n  starter               Download the first lesson project\n  status                Show the current lesson and project state\n\nWork in a project:\n  submit                Submit the committed Git revision\n  submission show       Show submission state and result\n  submission download   Download a saved revision ZIP\n  update                Apply the next available lesson transition\n  open                  Open the workspace or result in a browser\n\nSettings:\n  set api-url URL       Save the API address\n  set web-url URL       Save the web app address\n  set lang ru|en        Save the CLI language\n  set auto-checks true|false  Automatic checks in this project\n  config                Show effective settings\n\nMore help:\n  softpractice help <command>"))
	fmt.Fprintln(output, text(ctx,
		"\nВосстановление:\n  project restore       Восстановить продолжимый связанный проект",
		"\nRecovery:\n  project restore       Restore a resumable linked project"))
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
		russian, english = "Использование: softpractice status [--format text|json]\n\nПоказывает состояние текущего связанного Git-проекта.", "Usage: softpractice status [--format text|json]\n\nShows the state of the current linked Git project."
	case "submission":
		russian, english = "Использование: softpractice submission show [--id ID] [--format text|json|json-v2]\n              softpractice submission download --id ID [--output PATH]\n\nshow показывает безопасный статус и результат своей отправки. download сохраняет ZIP точной отправленной ревизии, как кнопка «Скачать проект» на странице результата.", "Usage: softpractice submission show [--id ID] [--format text|json|json-v2]\n       softpractice submission download --id ID [--output PATH]\n\nshow displays the safe status and result of your submission. download saves the exact submitted revision ZIP, equivalent to the result-page Download project button."
	case "submit":
		russian, english = "Использование: softpractice submit [--yes] [--checks=true|false]\n\nОтправляет чистый Git commit текущего урока на проверку.", "Usage: softpractice submit [--yes] [--checks=true|false]\n\nSubmits the clean Git commit for the current lesson."
	case "update":
		russian, english = "Использование: softpractice update\n\nПосле принятия решения применяет в этом же проекте переход к следующему уроку: добавляет, заменяет или удаляет только явно объявленные файлы.\n\nПереход применяется к принятому решению прошлого урока. Если текущий коммит — другой, команда ничего не меняет и показывает оба коммита и способ продолжить.", "Usage: softpractice update\n\nAfter acceptance, applies the next-lesson transition in the same project, adding, replacing, or removing only explicitly declared files.\n\nThe transition applies to the accepted solution of the previous lesson. When the current commit is a different one, the command changes nothing and shows both commits and how to continue."
	case "open":
		russian, english = "Использование: softpractice open [--web URL] [--no-browser]\n\nОткрывает workspace или последний результат в браузере.", "Usage: softpractice open [--web URL] [--no-browser]\n\nOpens the workspace or the latest result in a browser."
	case "set":
		russian, english = "Использование: softpractice set api-url|web-url|lang|auto-checks VALUE\n\nСохраняет пользовательскую настройку CLI. auto-checks true|false действует только в текущем проекте.", "Usage: softpractice set api-url|web-url|lang|auto-checks VALUE\n\nSaves a user-level CLI setting. auto-checks true|false applies only to the current project."
	case "config":
		russian, english = "Использование: softpractice config\n\nПоказывает действующие настройки и путь к конфигурации.", "Usage: softpractice config\n\nShows effective settings and the configuration path."
	default:
		return fmt.Errorf(text(ctx, "неизвестная команда %q", "unknown command %q"), command)
	}
	fmt.Fprintln(output, text(ctx, russian, english))
	return nil
}
