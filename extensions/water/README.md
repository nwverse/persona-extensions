# Water

Water (喝水提醒) sends timed drink-water reminders without calling a model. Log a cup directly from a notification or the 喝水 page, undo today's entries, and see daily totals for the last 7 or 30 days.

- [Extension guide and settings](../../docs/extensions/water.md)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md)
- [Manifest](persona-extension.json)

The package contains the `water` program, started with the hub. It keeps its own schedule and records in the extension data directory. No account is needed.

## Shortcuts and Siri

In Apple's **Shortcuts** app, add Persona's **Run Extension Action** and choose **记一杯水**. Leave the number empty to log one cup (`cup_ml`), or enter a whole number of milliliters from 1 to 3000. Each run logs a new drink, restarts the reminder interval, and requests a widget refresh. The result reports the amount and today's progress.

Name the shortcut **记一杯水** to run it by saying that name to Siri. On iPhone, you can assign the shortcut to the **Action button**. The same shortcut can run on Mac and Apple Watch; on the watch, Persona uses the watch's own connection to the hub.

Upgrading asks for consent again because the extension adds **Actions in Shortcuts and Siri** (快捷指令和 Siri 里的动作).

## Watch faces and widgets

On the iPhone Home Screen or Lock Screen, add a **Persona** widget and select **今天喝水**. On Apple Watch, edit a watch face and select the Persona **今天喝水** complication. This requires a hub that supports extension widgets. Tap a widget to open the 喝水 page.

Widgets show today's total and goal, with approximate cups left where space permits; medium widgets add the last seven days. Small and medium widgets request a ring: today's progress is drawn as a ring with the percentage in the center, and the seven-day chart in medium widgets sits beside it. Persona apps that support the progress ring draw the ring; older apps ignore the request and keep drawing the bar.

Circular complications show today's progress as drawn by the app. The ring applies only to small and medium widgets; watch faces and Lock Screen widgets keep their current drawings. There are no logging buttons or next-reminder times.

Logging or undoing a drink and crossing midnight in the plan's time zone request a refresh, with a regular 30-minute refresh as fallback. The hub limits background refresh notifications to once per device per widget in 15 minutes; the system can delay them further. Upgrading asks for consent again for permission to display content outside the app.
