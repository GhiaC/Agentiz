# Documents Components

This directory contains templ components for generating the Agentize Knowledge Tree documentation.

## Structure

- `styles.templ` - CSS styles
- `header.templ` - Page header component
- `stats.templ` - Statistics display component
- `search.templ` - Search box component
- `tree-view.templ` - Tree view component
- `detail-view.templ` - Detail view component
- `scripts.templ` - JavaScript code
- `page.templ` - Main page component that combines all components

## Setup

1. Install templ:
   ```bash
   go install github.com/a-h/templ/cmd/templ@latest
   ```

2. Add templ to go.mod:
   ```bash
   go get github.com/a-h/templ
   ```

3. Generate Go code from templ files:
   ```bash
   templ generate
   ```

## Usage

The components are used in `documents.go` via the `GenerateHTML()` method, which renders the `page` component with tree and nodes data.

