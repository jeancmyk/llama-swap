# llama-swap Setup Guide (Windows / Linux / macOS)

This guide gets llama-swap running on any OS with one portable config file.
No absolute paths live in the config — your machine-specific locations are
supplied through two environment variables, so the same YAML works everywhere.

---

## 1. Get llama-swap

### Option A — download a prebuilt release (easiest)

Releases are built automatically for `windows-amd64` (.zip), `linux-amd64`,
`linux-arm64`, `darwin-amd64` and `darwin-arm64` (.tar.gz):

1. Go to the fork's **Releases** page on GitHub.
2. Download the archive matching your OS/CPU.
3. Extract it anywhere, e.g. `C:\Tools\llama-swap\` or `/opt/llama-swap/`.

> Pushing a git tag (e.g. `v251`) to this repository triggers GitHub Actions
> (`.github/workflows/release.yml` + goreleaser) to publish these archives
> automatically with the UI embedded.

### Option B — build from source

Prerequisites:

| Tool  | Version  | Install hint                                  |
|-------|----------|-----------------------------------------------|
| Git   | any      | https://git-scm.com                            |
| Go    | >= 1.26 (**64-bit**; the 32-bit windows/386 build cannot compile it) | https://go.dev/dl/ |
| Node.js | >= 20 (with npm) | https://nodejs.org                 |

```bash
git clone https://github.com/jeancmyk/llama-swap
cd llama-swap

# build the web UI into internal/server/ui_dist
make ui          # runs: cd ui && npm install && npm run build

# build for your platform (UI embedded via -tags embed_ui)
make windows     # -> build/llama-swap-windows-amd64.exe
make linux-amd64 # -> build/llama-swap-linux-amd64
make mac         # -> build/llama-swap-darwin-arm64
```

Or without make, directly:

```bash
cd ui && npm install && npm run build && cd ..
go build -tags embed_ui -o build/llama-swap .
```

Windows tip: run the build from a normal PowerShell window. If you are behind
a restrictive shell where TLS downloads fail, use a plain HTTP-friendly
network or download Go/npm packages on another machine.

---

## 2. Get a backend server (llama.cpp example)

llama-swap manages *any* server that speaks OpenAI-style HTTP. The most common
is [llama.cpp](https://github.com/ggml-org/llama.cpp)'s `llama-server`:

- **Fastest**: download a prebuilt CUDA/Vulkan binary from the llama.cpp
  Releases page (`llama-bXXXX-bin-win-cuda-x64.zip`) and extract it.
- **From source**: build with CUDA support following llama.cpp's docs
  (`cmake -B build -DGGML_CUDA=ON && cmake --build build --config Release`),
  producing e.g. `build/bin/Release/llama-server.exe`.

You do **not** need llama.cpp if you already run vLLM, TabbyAPI, LM Studio,
ollama, etc. — see "Other backends" in `config.starter.yaml`.

---

## 3. Download models

Place GGUF files under one root folder, preserving the subfolder layout used
in the config, e.g.:

```
E:\LLM Models\                       (Linux example: /mnt/ai-ssd/LLM Models)
└── unsloth\
    ├── Qwen3-1.7B-GGUF\Qwen3-1.7B-BF16.gguf
    └── Qwen3.6-35B-A3B-MTP-GGUF\...
```

The starter config expects the Hugging Face repo folder names shown above;
edit paths freely to match your library.

---

## 4. Configure environment variables

| Variable           | Meaning                                   | Example value |
|--------------------|-------------------------------------------|---------------|
| `LLAMACPP_SERVER`  | full path to `llama-server` (incl. `.exe` on Windows) | `E:\llama.cpp\build\bin\Release\llama-server.exe` |
| `LLM_MODELS_DIR`   | root of your GGUF library                 | `E:\LLM Models` |

Set them persistently so every shell has them:

**Windows (PowerShell):**
```powershell
setx LLAMACPP_SERVER "E:\llama.cpp\build\bin\Release\llama-server.exe"
setx LLM_MODELS_DIR  "E:\LLM Models"
```

**Linux/macOS** (`~/.bashrc`, `~/.zshrc`):
```bash
export LLAMACPP_SERVER=/opt/llama.cpp/build/bin/llama-server
export LLM_MODELS_DIR="/mnt/ai-ssd/LLM Models"
```

---

## 5. Run it

Copy the starter config once and adjust model entries to your taste:

```bash
cp config.starter.yaml config.yaml
```

Then start:

```powershell
# Windows
.\scripts\start.ps1                    # uses config.starter.yaml by default
.\scripts\start.ps1 -Config config.yaml -Listen 127.0.0.1:8080
```

```bash
# Linux / macOS
./scripts/start.sh                     # or: ./scripts/start.sh config.yaml
```

Open **http://127.0.0.1:8080** for the web UI (model dashboard, playground,
activity, live config editor). The OpenAI-compatible API is at
`http://127.0.0.1:8080/v1`.

Quick test:

```bash
curl http://127.0.0.1:8080/v1/models
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3-1.7b-c32768","messages":[{"role":"user","content":"hi"}]}'
```

The first request to a model starts its process and waits for it to become
healthy (`healthCheckTimeout`, in seconds). Idle models unload after their
`ttl` expires; `ttl: 600` = 10 minutes.

---

## Why this config is OS agnostic

* `${env.LLAMACPP_SERVER}` / `${env.LLM_MODELS_DIR}` inject machine-specific
  absolute paths at load time.
* All other path segments use **forward slashes**, which every OS accepts,
  including Windows command lines and quoted arguments.
* Only OS-specific detail left to you: the binary name ends in `.exe` on
  Windows — that lives inside `LLAMACPP_SERVER`, not in the config.

For the full set of llama-swap options (profiles, selectors, matrix routing,
peers, hooks, filters...), see [`config.example.yaml`](config.example.yaml).
