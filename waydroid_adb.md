## ADB cheat sheet for Waydroid

Run this once per terminal. Replace `YOUR_IP:5555` with the exact serial from `devices -l`. The `ad` shortcut sends every command to that device.

```bash
ADB="$HOME/Documents/platform-tools/adb"
"$ADB" devices -l

SERIAL="YOUR_IP:5555"
ad() { "$ADB" -s "$SERIAL" "$@"; }
```

If Waydroid is missing from the list, run `"$ADB" connect "$SERIAL"` first. [Waydroid ADB guide](https://docs.waydro.id/faq/using-adb-with-waydroid)

### Find an app’s package name

| What you want                                         | Command                                                                             |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------- |
| List all installed packages                           | `ad shell pm list packages`                                                         |
| List user-installed apps                              | `ad shell pm list packages -3`                                                      |
| Search by part of the package name                    | `ad shell pm list packages settings`                                                |
| Find the app currently on screen                      | `ad shell dumpsys activity activities \| rg 'topResumedActivity\|mResumedActivity'` |
| Check whether a package is installed and find its APK | `ad shell pm path com.example.app`                                                  |

If the output says `package:com.android.settings`, use **`com.android.settings`** in a Launch App node. If the foreground activity looks like `com.android.settings/.Settings`, the package name is the part before `/`. [Android ADB reference](https://developer.android.com/tools/adb)

### Get screen size and coordinates

| What you want                                            | Command                                                      |
| -------------------------------------------------------- | ------------------------------------------------------------ |
| Physical and overridden size in pixels                   | `ad shell wm size`                                           |
| Display density                                          | `ad shell wm density`                                        |
| Current logical width × height in the active orientation | `ad shell dumpsys window displays \| rg 'mDisplayId=\|cur='` |
| Screenshot saved on Ubuntu                               | `ad exec-out screencap -p > screenshot.png`                  |

**For Tap coordinates, use `cur=` under `mDisplayId=0`.** If it shows `cur=1920x1080`, the valid screen coordinates go from `(0, 0)` at the top left to `(1919, 1079)` at the bottom right. `wm size` reports the physical and override sizes, which can differ from the current dimensions after rotation. [Android display dump](https://android.googlesource.com/platform/frameworks/base/+/refs/heads/android13-release/services/core/java/com/android/server/wm/DisplayContent.java#3392) · [Android `wm size` implementation](https://android.googlesource.com/platform/frameworks/base/+/a42e14ce6379/services/core/java/com/android/server/wm/WindowManagerShellCommand.java)

### Find a selector ID and its bounds

Open the screen you want to inspect, then run:

```bash
ad shell uiautomator dump /sdcard/window.xml
ad pull /sdcard/window.xml .
```

In `window.xml`, copy the full `resource-id`, such as `com.example.app:id/confirm`, into `selector_id`. `bounds="[100,200][300,260]"` describes a rectangle whose center is `(200, 230)`. [Android hierarchy dumper](https://android.googlesource.com/platform/frameworks/base/+/31fd8b3fa1eef849a4d5cfb6072d50606e3ba799/cmds/uiautomator/library/core-src/com/android/uiautomator/core/AccessibilityNodeInfoDumper.java)

### Other commands worth keeping

| Action                           | Command                                                                          |
| -------------------------------- | -------------------------------------------------------------------------------- |
| Install or update a test APK     | `ad install -r "$HOME/Downloads/test-app.apk"`                                   |
| Open Android Settings            | `ad shell am start -a android.settings.SETTINGS`                                 |
| Stop a test app                  | `ad shell am force-stop com.example.app`                                         |
| Tap a pixel                      | `ad shell input tap 120 300`                                                     |
| Swipe over 300 ms                | `ad shell input swipe 500 900 500 300 300`                                       |
| Press Back or Home               | `ad shell input keyevent KEYCODE_BACK` or `ad shell input keyevent KEYCODE_HOME` |
| Stream Android logs              | `ad logcat`                                                                      |
| Turn on Android pointer overlay  | `ad shell settings put system pointer_location 1`                                |
| Turn off Android pointer overlay | `ad shell settings put system pointer_location 0`                                |
| Dump the current UI tree         | `ad shell uiautomator dump /sdcard/window.xml`                                   |
| Pull the current UI tree         | `ad pull /sdcard/window.xml .`                                                   |

Direct ADB commands help you find and verify inputs. Run the JSON demo afterward to verify that **Runtime** produces the same action. [Android ADB reference](https://developer.android.com/tools/adb)
