# gorm-plugins

Behavioral plugins for [GORM v2](https://gorm.io/docs/).

This repository collects small, focused plugins that change how GORM behaves without forking the ORM or replacing its callback pipeline.

## Quick Start

Install the module:

```bash
go get github.com/d1agnoze/gorm-plugins
```

Then import the plugin you want and use its API directly.

Repository: https://github.com/d1agnoze/gorm-plugins

## Plugins

| Plugin | What it does |
|---|---|
| [txtracker](./txtracker/) | Tracks transaction nesting depth and runs hooks only after the outermost commit. |

## Contributing

Each plugin lives in its own package under the repo root. See the plugin README for installation and usage details.
