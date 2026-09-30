<p align="center">
  <a href="https://softpractice.ru">
    <img src="https://raw.githubusercontent.com/arthur-s/softpractice-cli/main/docs/assets/softpractice-logo-horizontal-navy.png" alt="SoftPractice" width="360">
  </a>
</p>

# SoftPractice CLI

[English](https://github.com/arthur-s/softpractice-cli/blob/main/README.md) | Русский

CLI-клиент [SoftPractice](https://softpractice.ru) — онлайн-тренажёра для
проектной практики программирования с AI-наставником.

SoftPractice позволяет работать над реалистичными задачами в локальных
Git-проектах, отправлять зафиксированные решения на проверку, получать обратную
связь и последовательно проходить уроки практикума. CLI связывает локальный
процесс разработки с workspace в SoftPractice.

## Установка

```bash
npm install -g @softpractice/softpractice-cli
```

Проверь установку:

```bash
softpractice --help
```

## Начало работы

Войди в SoftPractice:

```bash
softpractice login
```

Скачай starter-проект текущего практикума:

```bash
softpractice starter
```

Перейди в созданный каталог, проверь состояние проекта и прочитай задание:

```bash
cd <каталог-проекта>
softpractice status
softpractice task
softpractice material
```

Работай над проектом и фиксируй изменения в Git. Когда решение будет готово,
отправь текущий коммит и дождись результата:

```bash
softpractice submit --wait
```

Результат можно посмотреть снова в любой момент или открыть в браузере:

```bash
softpractice result
softpractice open result
```

После принятия решения примени переход к следующему доступному уроку:

```bash
softpractice update
```

`softpractice status` всегда заканчивается следующим шагом.

## Основные команды

| Команда | Описание |
| --- | --- |
| `softpractice login` | Войти через браузер |
| `softpractice starter` | Скачать проект первого урока |
| `softpractice status` | Показать состояние проекта и следующий шаг |
| `softpractice task` | Показать задание |
| `softpractice material` | Показать теоретический материал; `--lesson ID` — другого урока |
| `softpractice hint` | Показать открытые подсказки; следующая открывается на странице задания |
| `softpractice check` | Запустить публичные проверки текущего рабочего дерева |
| `softpractice submit` | Отправить текущую зафиксированную Git-ревизию; `--wait` дожидается результата |
| `softpractice result` | Показать результат проверки; `--wait` дожидается его, `--directions` добавляет рекомендации рецензента |
| `softpractice submissions` | Показать последние отправки урока |
| `softpractice update` | Применить переход к следующему уроку или перейти на новую версию текущего урока (заменяет только файлы урока, ваши файлы не трогает; до конца урока необязательно) |
| `softpractice open` | Открыть задание, материал или последний результат в браузере |
| `softpractice project restore` | Восстановить проект из последней подходящей ревизии |

Подробную справку можно получить командой `softpractice help <команда>`.

## Локальные проверки

Во время работы запускай публичные проверки вместе с незакоммиченными
изменениями:

```bash
softpractice check
```

Команда использует текущее рабочее дерево и ничего не отправляет на сервер.
Если проект использует виртуальное окружение Python, сначала активируй его,
чтобы команда `python3` из конфигурации разрешилась в нужный интерпретатор.

Чтобы перед каждой отправкой автоматически проверять точную зафиксированную
ревизию, включи автопроверки:

```bash
softpractice set auto-checks true
```

`softpractice submit --checks` включает такую проверку для одной отправки. В
отличие от `softpractice check`, проверка перед отправкой требует чистое рабочее
дерево и запускается в изолированном снимке `HEAD`.

## Вопросы рецензента

Если у рецензента есть вопросы к решению, `softpractice result` и
`softpractice status` сообщают об этом и дают ссылку на страницу результата:
отвечать нужно там.

## Машиночитаемый вывод

`status`, `task`, `material`, `hint`, `result`, `submissions` и `submit`
принимают `--json`. У каждого документа есть поле `kind`; при ошибке команда
выводит документ `softpractice.error`, в том числе при неверных аргументах
(код `usage`) и при отказе в подтверждении `submit` (код `declined`).

```bash
softpractice result --wait --timeout 10m --json
```

Коды выхода: `0` — готово, `1` — ошибка, `2` — неверные аргументы, `3` —
проверка ещё не готова, `4` — проверка заменена, результата не будет.

## Язык

SoftPractice CLI поддерживает английский и русский языки и по возможности
выбирает язык по системной локали.

Язык можно сохранить явно:

```bash
softpractice set lang en
softpractice set lang ru
```

Для одной команды язык можно переопределить флагом:

```bash
softpractice --lang en status
```

## Восстановление проекта

Если исходный каталог проекта недоступен, восстанови последнюю подходящую
ревизию в новый каталог:

```bash
softpractice project restore \
  --practicum <id-практикума> \
  --directory ./restored-project
```

CLI никогда не перезаписывает существующий каталог проекта.

## Ссылки

- [SoftPractice](https://softpractice.ru)
- [Исходный код](https://github.com/arthur-s/softpractice-cli)
- [Сообщить о проблеме](https://github.com/arthur-s/softpractice-cli/issues)

## Лицензия

[MIT](LICENSE)
