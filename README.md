# Softpractice CLI

`softpractice` is the cross-platform command-line companion for a Softpractice
learning workspace. It signs a learner in through a browser, downloads a
starter project, submits a committed Git revision, applies verified in-place
course updates, and opens the workspace or its latest result.

The CLI communicates only with the public Softpractice API. It contains no
evaluation prompts, hidden tests, course content, or server credentials.

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
