# MyApp Beta background reply contract

Build 15 uses a server job plus an iOS background `URLSession` result download. The ordinary website and legacy wrappers continue to use `POST /api/chat` and Bark.

## Create a job

`POST /api/native-replies` accepts the same JSON body as `/api/chat`. The Swift layer sends exact `Origin`, `X-MyApp-Client: MyAppBeta/15`, and a UUID `Idempotency-Key`. The server stores no API key, prompt, response body, or raw result token in the job table.

The response is HTTP 202 with `job_id`, `conversation_id`, `assistant`, `result_url`, and a short-lived `result_token`. Swift immediately gives the URL and token to its background session.

## Download the result

`GET /api/native-replies/{job_id}/result` requires the token in `X-MyApp-Reply-Token`. The server keeps the response alive while the existing chat pipeline generates and saves the reply. A completed result contains route identifiers and a 240-character reply preview for the local notification.

The Beta fetch adapter reloads the saved reply from `/api/history` while the app remains active. If iOS suspends the WebView, the background download still completes and schedules a local notification titled `Rhys` with `conversation_id` and `message_id`. The user can show the preview or replace it with a generic message in Beta settings.

## Persistence and limits

`native_reply_jobs` contains hashed idempotency keys and result tokens, status, route IDs, a bounded diagnostic error, and a 24-hour expiry. At most four unexpired jobs may be queued or running. Swift requests skip Bark through the server-owned `MyAppBeta/15` marker passed to the existing chat pipeline.

Force-quitting MyApp Beta cancels iOS background handling. Notifications for arbitrary server-initiated events still require APNs, Bark, or Web Push.
