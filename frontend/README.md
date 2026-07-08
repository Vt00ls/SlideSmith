# SlideSmith Frontend

Vite + React MVP UI for SlideSmith task creation, runtime progress,
confirmations, SVG preview, and PPTX download.

## Run

```bash
cd /Users/vt/Dev_space/slidesmith/frontend
npm install
npm run dev -- --port 5173
```

Default API proxy:

```text
http://10.2.37.236:18080
```

Override it when needed:

```bash
SLIDESMITH_API_PROXY=http://127.0.0.1:8080 npm run dev -- --port 5173
```

## Pages

```text
#/tasks
#/new
#/tasks/{taskId}
#/tasks/{taskId}/confirm
#/tasks/{taskId}/preview
```

