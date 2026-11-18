# Water

[Documentation](../README.md) · [Extension source](../../extensions/water/README.md)

Water (喝水提醒) reminds you to drink water and records each drink without calling a model or using an account. Open **喝水** in Persona to see today's progress, add a cup or another amount, undo today's entries, and compare daily totals for the last 7 or 30 days.

## Settings

Change these in **Settings › Extensions › 喝水提醒**:

| Setting | Default | Meaning |
| --- | --- | --- |
| `remind` | `true` | Send reminders; logging still works when disabled. |
| `goal_ml` | `2000` | Daily goal in milliliters, from 500 to 6000. |
| `cup_ml` | `250` | One cup in milliliters, from 50 to 1000. |
| `start` | `09:00` | Start of the daily reminder window, in HH:MM. |
| `end` | `21:00` | End of the window, inclusive; must be later than start. |
| `interval_minutes` | `60` | Minutes between reminders, from 15 to 240. |
| `weekends` | `true` | Also remind on Saturday and Sunday. |
| `stop_when_done` | `true` | Stop reminders for the day after reaching the goal. |

The plan uses the most recent valid time zone received from a page, widget, or shortcut, saved across restarts and shown on the page. Until then, it uses the user's device time zone reported by the hub at each start, falling back to the hub process's local time zone if neither is available. Days follow midnight boundaries in that zone, including daylight-saving changes.

The first reminder is at the window's start. Logging a drink restarts the interval. **等会儿** postpones the next reminder by 30 minutes; a later drink cancels that postponement. Occurrences outside the window move to the next allowed day's start. After downtime, at most one overdue reminder is sent within the current window. An invalid window disables reminders and shows a warning.

## Notifications

Notifications replace the previous water reminder and offer **喝了一杯**, **等会儿**, and **记别的量**. The last button accepts an amount such as `300`, `300ml`, or `300 毫升` (1–3000). Repeating the same notification button does not log twice; page buttons can be used repeatedly. Notification buttons are currently drawn only by the Apple clients; other clients show a plain notification. Real-device delivery depends on Persona and the device's notification settings.

Reminders use ordinary urgency (P1). With low-distraction mode enabled, they are held for briefings and do not ring; the page shows a warning when the hub reports that mode. Water never raises them to P0. If notification permission is missing, the page explains why reminders cannot be sent and logging remains available.

A connection failure or server error gets at most three attempts in total, 30 seconds apart, then the normal interval resumes. A hub response saying the notification was not sent is recorded without retrying; the page shows the reason. Attempts survive a process restart. An unknown low-distraction status does not prevent using the page.

## Shortcuts and Siri

In Apple's **Shortcuts** app, add Persona's **Run Extension Action** and choose **记一杯水**. Leave the number empty to log one cup (`cup_ml`), or enter a whole number of milliliters from 1 to 3000. Each run logs a new drink, restarts the reminder interval, and requests a widget refresh. The result reports the amount and today's progress.

Name the shortcut **记一杯水** to run it by saying that name to Siri. On iPhone, you can assign the shortcut to the **Action button**. The same shortcut can run on Mac and Apple Watch; on the watch, Persona uses the watch's own connection to the hub.

Upgrading asks for consent again because the extension adds **Actions in Shortcuts and Siri** (快捷指令和 Siri 里的动作).

## Watch faces and widgets

On the iPhone Home Screen or Lock Screen, add a **Persona** widget and select **今天喝水**. On Apple Watch, edit a watch face and choose the Persona **今天喝水** complication. A hub that supports extension widgets is required. Tapping a widget opens the 喝水 page.

Small widgets show today's total, goal, and approximate cups left. Medium widgets add daily totals for the last seven days. Small and medium widgets request a ring: today's progress is drawn as a ring with the percentage in the center, and the seven-day chart in medium widgets sits beside it. Persona apps that support the progress ring draw the ring; older apps ignore the request and keep drawing the bar.

Circular complications show today's progress as drawn by the app, rectangular ones show progress with a short summary, and inline ones show a single sentence. The ring applies only to small and medium widgets; watch faces and Lock Screen widgets keep their current drawings. Widgets have no logging buttons and do not show the next reminder time.

Logging or undoing a drink requests a refresh, as does crossing midnight in the plan's time zone. A regular 30-minute refresh is the fallback. The hub limits background refresh notifications to once per device per widget in 15 minutes, and the system can delay updates further, so displayed progress can lag a drink logged from a notification.

Upgrading asks for consent again because the extension requests permission to display content outside the app.

## Data and scope

Records and reminder state live only in the hub's extension data directory, in an atomically replaced private file. Records are kept for the most recent 400 calendar days. A damaged file is backed up there and a fresh file is started. Uninstalling the extension deletes its data directory, including backups. No data is sent to an account or health service.

Water includes reminders, notification buttons, the page, settings, widgets, watch complications, and a Shortcuts and Siri action. It does not include assistant skills, MCP tools, overview cards, Apple Health sync, meeting-aware postponement, or translations.
