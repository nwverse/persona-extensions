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

The plan uses the most recent valid time zone received when the page is opened or an action is taken. That zone is saved and shown on the page. Until then, the hub process's local time zone is used. Days follow local midnight boundaries, including daylight-saving changes.

The first reminder is at the window's start. Logging a drink restarts the interval. **等会儿** postpones the next reminder by 30 minutes; a later drink cancels that postponement. Occurrences outside the window move to the next allowed day's start. After downtime, at most one overdue reminder is sent within the current window. An invalid window disables reminders and shows a warning.

## Notifications

Notifications replace the previous water reminder and offer **喝了一杯**, **等会儿**, and **记别的量**. The last button accepts an amount such as `300`, `300ml`, or `300 毫升` (1–3000). Repeating the same notification button does not log twice; page buttons can be used repeatedly. Notification buttons are currently drawn only by the Apple clients; other clients show a plain notification. Real-device delivery depends on Persona and the device's notification settings.

Reminders use ordinary urgency (P1). With low-distraction mode enabled, they are held for briefings and do not ring; the page shows a warning when the hub reports that mode. Water never raises them to P0. If notification permission is missing, the page explains why reminders cannot be sent and logging remains available.

A connection failure or server error gets at most three attempts in total, 30 seconds apart, then the normal interval resumes. A hub response saying the notification was not sent is recorded without retrying; the page shows the reason. Attempts survive a process restart. An unknown low-distraction status does not prevent using the page.

## Data and scope

Records and reminder state live only in the hub's extension data directory, in an atomically replaced private file. Records are kept for the most recent 400 calendar days. A damaged file is backed up there and a fresh file is started. Uninstalling the extension deletes its data directory, including backups. No data is sent to an account or health service.

This version includes reminders, notification buttons, the page, and settings. It does not include widgets, watch complications, Shortcuts, assistant skills, MCP tools, overview cards, Apple Health sync, meeting-aware postponement, or translations.
