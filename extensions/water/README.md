# Water

Water (喝水提醒) sends timed drink-water reminders without calling a model. Log a cup directly from a notification or the 喝水 page, undo today's entries, and see daily totals for the last 7 or 30 days.

- [Extension guide and settings](../../docs/extensions/water.md)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md)
- [Manifest](persona-extension.json)

The package contains the `water` program, started with the hub. It keeps its own schedule and records in the extension data directory. No account is needed.

## Watch faces and widgets

On the iPhone Home Screen or Lock Screen, add a **Persona** widget and select **今天喝水**. On Apple Watch, edit a watch face and select the Persona **今天喝水** complication. This requires a hub that supports extension widgets. Tap a widget to open the 喝水 page.

Widgets show today's total and goal, with approximate cups left where space permits; medium widgets add the last seven days. Circular complications show today's progress as drawn by the app. There are no logging buttons or next-reminder times.

Logging or undoing a drink and crossing midnight in the plan's time zone request a refresh, with a regular 30-minute refresh as fallback. The hub limits background refresh notifications to once per device per widget in 15 minutes; the system can delay them further. Upgrading asks for consent again for permission to display content outside the app.
