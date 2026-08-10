# CLI: первый пользовательский цикл

`softpractice` реализует минимальный цикл ученика: вход через браузер,
просмотр текущего задания, отправку зафиксированного состояния Git-репозитория
и открытие рабочего пространства в web-приложении.

## Сборка

```bash
go build -o softpractice ./cmd/softpractice
```

Go соберёт весь пакет `cmd/softpractice` вместе с его вспомогательными файлами.
В Windows итоговый файл будет называться `softpractice.exe`.

По умолчанию CLI обращается к `http://localhost:8080`. Другой API задаётся
один раз в пользовательской конфигурации:

```bash
./softpractice set api-url https://local.softpractice.ru
./softpractice set web-url https://local.softpractice.ru
```

Её можно посмотреть командой `./softpractice config`. Конфигурация хранит
только адреса и язык: в Linux это обычно
`~/.config/softpractice/config.json`, в macOS — `~/Library/Application Support/softpractice/config.json`,
в Windows — `%AppData%\softpractice\config.json`. Флаг `--api` перед именем
команды и переменная `SOFTPRACTICE_API_URL` временно переопределяют это
значение. Удалённый API обязан использовать HTTPS; HTTP разрешён только для
loopback.

Язык CLI выбирается по системной локали (`ru` или `en`), а если определить её
не удалось — английский. Его можно сохранить явно:

```bash
./softpractice set lang ru
```

Для одного запуска доступен флаг `--lang ru|en`; переменная
`SOFTPRACTICE_LANGUAGE` имеет приоритет над конфигурацией.

## Вход

```bash
./softpractice login
```

CLI запускает device authorization flow, показывает одноразовый код и открывает
отдельную страницу `/cli/connect` в браузере. Код уже находится в ссылке и
подставляется на странице автоматически; CLI ждёт решения и завершает вход сам.
Для окружений без GUI:

```bash
./softpractice login --no-browser
```

Refresh-токен хранится только в системном хранилище секретов: macOS Keychain,
Windows Credential Manager или Linux Secret Service. Access-токен существует
только в памяти процесса. Файл `softpractice/credentials.json` содержит
несекретные метаданные сессии; каталог создаётся с правами `0700`, файл —
`0600`. При обновлении сессии новый refresh-токен сначала сохраняется в
keyring, после чего атомарно обновляются метаданные.

Веб-аккаунт использует email и пароль. После регистрации email подтверждается
одноразовым кодом; в локальной среде он приходит в Mailpit: `http://localhost:18025`.

## Текущее состояние

```bash
./softpractice status
./softpractice status --format json
```

Команда запускается внутри связанного Git-репозитория и читает
`.softpractice/project.json`. Она выводит пользователя, проект, workspace,
текущее задание, локальный `HEAD`, чистоту рабочей директории и последнюю
отправку. Сервер повторно проверяет владение workspace и соответствие
`project_id`.

JSON-режим возвращает версионированную learner-safe проекцию без email,
локальных путей, токенов и серверной диагностики. Она предназначена для
автоматизации поверх того же публичного CLI.

## Результат отправки

```bash
./softpractice submission show --format json
./softpractice submission show --id SUBMISSION_ID --format json
./softpractice submission show --id SUBMISSION_ID --format json-v2
```

Команда опрашивает публичный evaluation endpoint. Для `queued`/`leased` она
возвращает `terminal: false` и `next_poll_seconds`; для terminal-состояния —
ограниченную проекцию статуса, deterministic outcome, review и безопасного
technical support code. Hidden evidence, runner output и provider diagnostics
в контракт не входят.

`json` сохраняет компактный contract version 1 для существующей автоматизации.
`json-v2` возвращает version 2 и передаёт без потери всю learner-safe проекцию
`evaluation`: deterministic checks и расширенный review result. Он предназначен
для QA-оракулов, которым недостаточно terminal verdict.

## Отправка решения

Запускать из чистого Git-репозитория:

```bash
./softpractice submit
```

CLI требует отсутствие tracked и untracked изменений, создаёт `tar.gz` через
`git archive HEAD` и отправляет его вместе с SHA коммита, текущей assignment,
base revision и детерминированным idempotency key. Повторная отправка того же
commit безопасно воспроизводит исходный запрос. Перед загрузкой CLI показывает
assignment, commit, число файлов и размер архива и запрашивает подтверждение.
Для автоматизированного запуска используется:

```bash
./softpractice submit --yes
```

Assignment и её версия всегда приходят с сервера и не могут быть выбраны
локальным флагом. До загрузки CLI отклоняет symlink, Git submodule и превышение
лимитов, а также предупреждает о потенциально чувствительных файлах.

## Следующий урок в том же проекте

После принятого `pa-foundation-01` останьтесь в той же папке и выполните:

```bash
./softpractice update
```

CLI сверяет канонический хеш локального `HEAD` с принятой ревизией, скачивает
проверяемый пакет перехода и добавляет файлы следующего урока в тот же
репозиторий. Он заменяет `decision.md` шаблоном текущего урока и создаёт
технический Git commit; код предыдущего решения остаётся в истории. Если
локальный commit не совпадает с принятым, команда ничего не меняет.

## Открытие результата

```bash
./softpractice open
```

Если отправка уже существует, команда открывает её страницу; иначе — workspace.
Адрес web-приложения задаётся через `softpractice set web-url`, флаг `--web`
или `SOFTPRACTICE_WEB_URL`. Флаг `--no-browser` только печатает итоговый URL.

## Завершение CLI-сессии

```bash
./softpractice logout
```

Команда отзывает refresh-токен через публичный API, удаляет его из системного
keyring и удаляет локальные несекретные metadata. QA-runner использует отдельный
credential namespace на каждый прогон и завершает его этой командой.
