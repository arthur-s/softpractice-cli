<p align="center">
  <a href="https://softpractice.ru">
    <img src="https://raw.githubusercontent.com/arthur-s/softpractice-cli/main/docs/assets/softpractice-logo-horizontal-navy.png" alt="SoftPractice" width="360">
  </a>
</p>

# SoftPractice CLI

The command-line companion for [SoftPractice](https://softpractice.ru) — an
online platform for project-based programming practice with an AI mentor.

SoftPractice lets you work on realistic programming tasks in local Git
projects, submit committed solutions for review, receive feedback, and continue
through a practicum lesson by lesson. The CLI connects your local development
workflow to your SoftPractice workspace.

> SoftPractice — онлайн-тренажёр по программированию с AI-наставником,
> построенный вокруг практической работы над проектами. CLI позволяет получать
> задания, отправлять решения и переходить между уроками прямо из терминала.

## Install

```bash
npm install -g @softpractice/softpractice-cli
```

Verify the installation:

```bash
softpractice --help
```

## Getting started

Sign in to SoftPractice:

```bash
softpractice login
```

Download the starter project for your current practicum:

```bash
softpractice starter
```

Open the created directory and check the current lesson:

```bash
cd <project-directory>
softpractice status
```

Work on the project and commit your changes with Git. When the solution is
ready, submit the current commit:

```bash
softpractice submit
```

Check the submission and open its result:

```bash
softpractice submission show
softpractice open
```

After an accepted solution, apply the transition to the next available lesson:

```bash
softpractice update
```

## Common commands

| Command | Description |
| --- | --- |
| `softpractice login` | Sign in through the browser |
| `softpractice starter` | Download the first lesson project |
| `softpractice status` | Show the current lesson and local project state |
| `softpractice submit` | Submit the current committed Git revision |
| `softpractice submission show` | Show the latest submission and review result |
| `softpractice update` | Apply the transition to the next lesson |
| `softpractice open` | Open the workspace or latest result in the browser |
| `softpractice project restore` | Restore a project from the latest usable revision |

Run `softpractice help <command>` for detailed help.

## Language

SoftPractice CLI supports English and Russian and selects a language from the
system locale when possible.

Choose a language explicitly:

```bash
softpractice set lang en
softpractice set lang ru
```

You can also override it for a single command:

```bash
softpractice --lang ru status
```

## Project recovery

If the original project directory is unavailable, restore the latest usable
revision into a new directory:

```bash
softpractice project restore \
  --practicum <practicum-id> \
  --directory ./restored-project
```

The CLI never overwrites an existing project directory.

## Links

- [SoftPractice](https://softpractice.ru)
- [Source code](https://github.com/arthur-s/softpractice-cli)
- [Issues](https://github.com/arthur-s/softpractice-cli/issues)

## License

[MIT](LICENSE)
