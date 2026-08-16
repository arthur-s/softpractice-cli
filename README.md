# Softpractice CLI

`softpractice` is the cross-platform command-line companion for a Softpractice
learning workspace. It signs a learner in through a browser, downloads a
starter project, submits a committed Git revision, applies verified in-place
course updates, and opens the workspace or its latest result.

The CLI communicates only with the public Softpractice API. It contains no
evaluation prompts, hidden tests, course content, or server credentials.

## Restore a project to continue a practicum

If the original local directory is unavailable, recreate a linked Git project
from the learner's latest usable revision:

```bash
softpractice project restore \
  --practicum equipment-rental-python \
  --directory ./equipment-rental-python
```

The restore is placed only into a new directory. The CLI prefers the latest
submission of the current lesson. If the current lesson has no submission yet,
it restores the accepted predecessor and applies the ordinary authorized
course update. With no submission history it uses the current published
starter.

## Download a submitted project

The result page URL contains the submission ID. Download that exact immutable
revision as a ZIP with:

```bash
softpractice submission download --id 3caf6517-915f-4e92-819a-aa9fa4cae367
```

Use `--output PATH` to choose a different ZIP destination. The command only
downloads revisions owned by the signed-in learner and never overwrites an
existing file.

## Install from source

```bash
go install github.com/arthur-s/softpractice-cli/cmd/softpractice@latest
```

## Configure

```bash
softpractice set api-url https://your.softpractice.example
softpractice set web-url https://your.softpractice.example
softpractice login
```

Run `softpractice --help` to see all commands. Configuration stores only API
addresses and language preference; refresh credentials remain in the operating
system keyring.

## License

[MIT](LICENSE)
