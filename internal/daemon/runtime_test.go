package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureDockerScaffold_Go(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644))

	configPath, err := ensureDockerScaffold(dir)
	require.NoError(t, err)
	assert.Equal(t, "docker-compose.yml", configPath)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM golang:1.22-bookworm")
	assert.Contains(t, string(df), "go mod download")

	compose, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(compose), "services:")
	assert.Contains(t, string(compose), "command: tail -f /dev/null")

	ignore, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	require.NoError(t, err)
	assert.Contains(t, string(ignore), "node_modules")
}

func TestEnsureDockerScaffold_Node(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0644))

	_, err := ensureDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM node:22-bookworm")
	assert.Contains(t, string(df), "npm ci")
}

func TestEnsureDockerScaffold_NodeWorkspaceAwareInstall(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"workspaces\":[\"apps/*\"]}\n"), 0644))

	_, err := ensureDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), `grep -q '"workspaces"' package.json`)
	assert.Contains(t, string(df), `sed -i 's/"workspace:[^"]*"/"*"/g'`)
	assert.Contains(t, string(df), "npm install --workspaces --include-workspace-root")
	assert.Contains(t, string(df), "npm ci --workspaces --include-workspace-root")
	assert.Contains(t, string(df), "|| npm install --workspaces --include-workspace-root")
}

func TestEnsureDockerScaffold_Python(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("pytest\n"), 0644))

	_, err := ensureDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM python:3.12-slim")
	assert.Contains(t, string(df), "pip install -r requirements.txt")
}

func TestEnsureDockerScaffold_Rust(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"x\"\nversion=\"0.1.0\"\n"), 0644))

	_, err := ensureDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM rust:1.78-bookworm")
	assert.Contains(t, string(df), "cargo fetch")
}

func TestEnsureDockerScaffold_Laravel(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "composer.json"), []byte("{}\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artisan"), []byte("#!/usr/bin/env php\n"), 0644))

	_, err := ensureDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM php:8.3-cli-bookworm")
	assert.Contains(t, string(df), "COPY --from=composer:2")
	assert.Contains(t, string(df), "docker-php-ext-install")
}

func TestEnsureDockerScaffold_Spring(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project></project>\n"), 0644))

	_, err := ensureDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM maven:3.9-eclipse-temurin-21")
	assert.Contains(t, string(df), "dependency:go-offline")
}

func TestEnsureDockerScaffold_DoesNotOverwriteExistingDockerfile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0644))

	_, err := ensureDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Equal(t, "FROM alpine:3.20\n", string(df))
}

func TestRecreateDockerScaffold_OverwritesExistingDockerFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(".git\n"), 0644))

	_, err := RecreateDockerScaffold(dir)
	require.NoError(t, err)

	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM golang:1.22-bookworm")
	assert.NotContains(t, string(df), "FROM alpine:3.20")

	compose, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(compose), "command: tail -f /dev/null")

	ignore, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	require.NoError(t, err)
	assert.Contains(t, string(ignore), "node_modules")
}
