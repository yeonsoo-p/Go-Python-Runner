.PHONY: proto bindings test test-go test-integration test-python test-frontend lint lint-go lint-python lint-frontend dev build bundle-python installer clean

# --- Code Generation ---

proto:
	uv run python -m grpc_tools.protoc -Iproto \
		--go_out=. --go-grpc_out=. \
		--go_opt=module=go-python-runner --go-grpc_opt=module=go-python-runner \
		proto/runner.proto
	uv run python -m grpc_tools.protoc -Iproto \
		--python_out=scripts/_lib/gen --grpc_python_out=scripts/_lib/gen \
		proto/runner.proto
	uv run python -c "import pathlib; f=pathlib.Path('scripts/_lib/gen/runner_pb2_grpc.py'); f.write_text(f.read_text().replace('import runner_pb2 as', 'from . import runner_pb2 as'))"

bindings:
	wails3 generate bindings

# --- Testing ---

test: test-go test-python test-frontend test-integration

test-go:
	go test ./internal/...

test-integration:
	go test ./tests/integration/ -tags=integration -v -timeout=120s

test-python:
	uv run pytest scripts/_lib/tests/

test-frontend:
	cd frontend && npx vitest run

# --- Linting ---

lint: lint-go lint-python lint-frontend

lint-go:
	go vet ./...

lint-python:
	uv run ruff check --fix scripts/ tests/
	uv run ruff format scripts/ tests/
	uv run mypy scripts/_lib/runner.py
	uv run mypy scripts/_lib/tests

lint-frontend:
	cd frontend && npx tsc --noEmit

# --- Development ---

dev:
	wails3 dev

# --- Build ---

build:
	cd frontend && npm run build
	wails3 build
	uv run python -c "import shutil,os; dst='bin/scripts'; shutil.rmtree(dst,True); shutil.copytree('scripts',dst,ignore=shutil.ignore_patterns('tests','__pycache__'))"
	uv run python -c "import shutil,os; dst='bin/python'; shutil.rmtree(dst,True); shutil.copytree('python',dst) if os.path.isdir('python') else None"

bundle-python:
	uv run python build/bundle_python.py

installer: proto bundle-python build
	wails3 task windows:create:nsis:installer

# --- Cleanup ---

clean:
	go clean -cache
	uv run python -c "import shutil, os; [shutil.rmtree(p) for p in ['frontend/dist', 'bin'] if os.path.isdir(p)]"
