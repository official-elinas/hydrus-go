



# Detailed Guide: LLM-Driven Migration of Hydrus Network (Python to Golang)

The [Hydrus Network](https://github.com/hydrusnetwork/hydrus) is a highly complex, mature personal booru-style media tagging application built in Python. It relies heavily on PyQt for its graphical interface, SQLite for its robust database backend, and a custom client-server networking model to synchronize tags and files. 

Converting a monolithic Python codebase of this scale into **Golang** to harden it, improve concurrency, and ensure deployment stability is a monumental task. An LLM (or a system of specialized LLM Agents) must approach this migration with a structured, phased methodology rather than line-by-line translation.

Below is a comprehensive blueprint for orchestrating LLM agents to tackle the Python-to-Golang migration of the Hydrus Network.

---

## Phase 1: Architectural Comprehension & Context Ingestion

Before any code is generated, the LLM needs to build a mental map of the Hydrus architecture. Because the repository is massive, a Retrieval-Augmented Generation (RAG) system or an Abstract Syntax Tree (AST) parsing tool must feed the LLM chunked contexts.

The LLM must map the system into four distinct domains:

1.  **The Database Layer (SQLite Heart):**
    *   **Context:** Hydrus is essentially a massive wrapper around an SQLite database. It manages multiple "domains" (e.g., `local files`, `trash`, `my tags`, `IPFS`) identified by service keys (hex strings like `6c6f63616c2066696c6573`). 
    *   **LLM Task:** Identify database schema creation scripts and the massive monolithic DB query objects (e.g., the master file search function, which historically spanned ~1800 lines of Python).
2.  **The File Storage & Processing Layer:**
    *   **Context:** Hydrus hashes all files via SHA256 and stores them on the disk using the first two characters of the hash as folder names. It relies on `FFmpeg` and image parsers.
    *   **LLM Task:** Identify hashing mechanisms, file I/O operations, and Python-specific media processing libraries (OpenCV/Pillow) that will need Go equivalents.
3.  **The Concurrency & Job Scheduling Layer:**
    *   **Context:** Python's Global Interpreter Lock (GIL) limits true multi-threading. Hydrus relies on background threads, queues, and twisted engine networking.
    *   **LLM Task:** Map Python `threading.Thread`, `queue.Queue`, and asynchronous loops to Golang's `goroutines` and `channels`.
4.  **The GUI & API Layer (The Monolith Problem):**
    *   **Context:** Hydrus heavily mixes PyQt5/Qt6 UI logic with database logic. 
    *   **LLM Task:** The LLM must recognize that Go is not ideal for native desktop GUIs. The agentic system must pivot the architecture: **extract the core into a Headless Go Daemon** and expose a local REST/gRPC API for either a web frontend, a Wails (Go+Web) app, or a lightweight Python/Qt UI layer.

---

## Phase 2: Multi-Agent Team Structure

To execute this, assign specialized system prompts (personas) to concurrent LLM agents.

*   **🕵️ Architect Agent:** Analyzes the Python directory structure. Defines the Go package structure (`cmd/`, `internal/db/`, `internal/api/`, `internal/media/`). Coordinates the sub-agents.
*   **🗄️ Database/State Agent:** Solely responsible for translating SQLite commands. In Go, they will implement `mattn/go-sqlite3` or `zombiezen/go-sqlite` and handle connection pooling.
*   **⚙️ Concurrency & Backend Agent:** Translates the heavy business logic, file hashing, background cleanup tasks, and network synchronization protocols into Go idioms.
*   **🖥️ Interface & API Agent:** Wraps the backend functions into an HTTP server (using `gin-gonic/gin` or `go-chi/chi`), mimicking the existing Hydrus Client API (port 45869).
*   **🧪 QA & Validation Agent:** Generates Go tests (`_test.go`) and creates scripts to compare the output of the Python program with the Go program using the same test database.

---

## Phase 3: Step-by-Step Agent Execution Plan

### Step 1: Schema Extraction and Data Mapping
The Database Agent must extract all SQL `CREATE TABLE` statements from the Python codebase.
*   **Python Target:** `hydrus/client/db` and `hydrus/server/db` modules.
*   **Go Action:** Create Go structs to represent the data. Because Hydrus operates on an update-driven "Service" model (domains mapped to IDs like 0=tag repository, 1=file repository, 15=local file storage), the agent must define these services as Enums in Go.
*   **Hardening Strategy:** Use Go's `database/sql` combined with a query builder like `squirrel` or an ORM like `gorm`, ensuring prepared statements are strictly enforced to prevent injection and memory leaks.

### Step 2: Re-architecting File Management
The Backend Agent handles the physical storage format.
*   **Python Target:** `hydrus/client/import` and media hashing scripts.
*   **Go Action:** Write an `internal/storage` package.
    *   Implement robust concurrent file hashing (SHA256) leveraging `io.Copy` and Go's `crypto/sha256`.
    *   Implement Go wrappers using `os/exec` to call `ffmpeg` binaries (which Hydrus distributes locally) to parse media metadata (duration, resolution, audio channels).

### Step 3: Translating Concurrency to Goroutines
Hydrus performs extensive background work: parsing new tags, maintaining the local booru, syncing with community servers, and cleaning up deleted files.
*   **Python Target:** "Job" loops and `threading` modules.
*   **Go Action:** Replace Python thread pools with a robust Go Worker Pool pattern.
    ```go
    // Example conceptual output by the Backend Agent
    type Job interface { Execute() error }
    
    func WorkerPool(jobs <-chan Job, results chan<- error, workers int) {
        var wg sync.WaitGroup
        for i := 0; i < workers; i++ {
            wg.Add(1)
            go func() {
                defer wg.Done()
                for job := range jobs {
                    results <- job.Execute()
                }
            }()
        }
        wg.Wait()
    }
    ```
*   **Hardening Strategy:** Python processes can crash silently or hang due to queue locks. The LLM must implement Go `context.Context` to handle graceful shutdowns, timeouts, and cancellations.

### Step 4: The Network Protocols & API
Hydrus features a complex custom synchronization protocol to share tags across servers. It also has a local REST API.
*   **Python Target:** Network code handling Hydrus Services API, JSON serialization, and hex key authentication.
*   **Go Action:** The API Agent will map out the existing `/get_services`, `/add_tags`, and `/search_files` endpoints. Go's strict static typing (`encoding/json`) will instantly harden the loosely-typed dictionary data payloads originally used in Python.

### Step 5: Decoupling the GUI (The Masterstroke)
Instead of trying to force Go into a PyQt5 shape (which leads to unstable CGO dependencies), the Architect Agent must dictate a strict Backend-Frontend separation.
*   **Action Plan:** The LLM rewrites the *entire* Hydrus core as a high-performance Go Daemon. 
*   **The Frontend:** The LLM can generate a new web-based frontend using React/Vue, or wrap the Go binary in **Wails** (a Go framework that provides a web-view window, operating exactly like an Electron/desktop app but natively executing Go backend functions).

---

## Phase 4: Prompts & Orchestration Strategy for LLMs

To keep the LLM focused and prevent hallucination, prompts must be highly specific and bounded by context sizes.

**Prompt Example for the Database Agent:**
> *"You are the Database Agent translating the Hydrus Network from Python to Go. I will provide you with a chunk of Python code containing an SQLite schema and query logic for the 'Master File Search'. Your task is to: 1. Convert the schema to Go struct definitions. 2. Write an interface defining the CRUD operations. 3. Write an implementation using 'database/sql'. Ensure thread-safe connection pooling is configured, as Go handles concurrency fundamentally differently than Python. Output only idiomatic Go code and a brief explanation of how you handled the JOIN operations."*

**Prompt Example for the Architect Agent:**
> *"Review this Python threading loop used in Hydrus to check for deleted files. Re-architect this for Golang. Do not just translate it line-by-line; instead, design a robust pattern using Go channels, select statements, and context.Context for graceful degradation. Ensure that if the SQLite database gets locked, the goroutine backs off exponentially."*

---

## Phase 5: Testing & "Shadowing" Strategy

Because users' personal Hydrus databases are vast and contain years of metadata, data corruption is unacceptable. The QA Agent must instruct the LLM to build a **Shadow Testing Suite**.

1.  **Binary Parity Testing:** The LLM writes test scripts that start the Python version and the new Go version side-by-side using a copy of an identical test database.
2.  **API Replay:** The script replays a sequence of actions (import file, tag file, search file, sync to server).
3.  **State Verification:** After actions are processed, the script diffs the SQLite databases bit-by-bit. If the Go SQLite driver structures the data differently or handles transaction locking poorly compared to the Python `sqlite3` DB-API 2.0, the test fails, and the DB Agent is prompted to fix the code.

By enforcing strict typing, context-driven concurrency, and complete UI/Backend separation, this LLM-orchestrated approach will convert Hydrus from a heavyweight Python application into a highly concurrent, memory-safe, and stable Golang native application.