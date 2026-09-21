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

Перейди в созданный каталог и проверь текущий урок:

```bash
cd <каталог-проекта>
softpractice status
```

Работай над проектом и фиксируй изменения в Git. Когда решение будет готово,
отправь текущий коммит:

```bash
softpractice submit
```

Проверь состояние отправки и открой результат:

```bash
softpractice submission show
softpractice open
```

После принятия решения примени переход к следующему доступному уроку:

```bash
softpractice update
```

## Основные команды

| Команда | Описание |
| --- | --- |
| `softpractice login` | Войти через браузер |
| `softpractice starter` | Скачать проект первого урока |
| `softpractice status` | Показать текущий урок и состояние локального проекта |
| `softpractice check` | Запустить публичные проверки текущего рабочего дерева |
| `softpractice submit` | Отправить текущую зафиксированную Git-ревизию |
| `softpractice submission show` | Показать последнюю отправку и результат проверки |
| `softpractice update` | Применить переход к следующему уроку |
| `softpractice open` | Открыть workspace или последний результат в браузере |
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
