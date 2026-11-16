# Waydroid command cheat sheet for Ubuntu

Use `waydroid` to manage the emulator and ADB to work with Android inside it.

## Start, stop, and troubleshoot Waydroid

| Command                                     | What it does                                                                     |
| ------------------------------------------- | -------------------------------------------------------------------------------- |
| `waydroid show-full-ui`                     | Opens the full Android screen.                                                   |
| `waydroid status`                           | Shows the session, container state, and IP address.                              |
| `waydroid session start`                    | Starts the Android session without opening the full UI.                          |
| `waydroid session stop`                     | Stops the current session.                                                       |
| `sudo systemctl restart waydroid-container` | Restarts the container service. Open Waydroid again afterward.                   |
| `waydroid log -n 100`                       | Shows recent Waydroid logs and follows new entries.                              |
| `sudo waydroid shell`                       | Opens a shell inside Android, even when ADB can’t connect. Type `exit` to leave. |
| `sudo waydroid logcat`                      | Streams Android app and system logs. Press `Ctrl+C` to stop.                     |

If the IP is `UNKNOWN`, open the full UI first. You can also run `ip -4 addr show eth0` inside `sudo waydroid shell` to check whether Android actually has an IPv4 address. [Waydroid CLI reference](https://docs.waydro.id/usage/waydroid-command-line-options)

## Install and launch apps

```bash
waydroid app install "$HOME/Downloads/test-app.apk"
waydroid app list
waydroid app launch com.example.app
```

Replace `com.example.app` with the package name shown in the app list. For example, `waydroid app launch com.android.settings` opens Android Settings. [Waydroid app guide](https://docs.waydro.id/usage/install-and-run-android-applications)

## Connect ADB and inspect Android

Use your current Waydroid IP in `SERIAL`. Keep the `-s "$SERIAL"` argument when multiple devices are connected.

```bash
ADB="$HOME/Documents/platform-tools/adb"
SERIAL="YOUR_IP:5555"

"$ADB" connect "$SERIAL"
"$ADB" devices -l
```

Continue when the serial appears with state `device`. Your installed Waydroid also supports `waydroid adb connect`, which uses the `adb` found on your `PATH`. [Waydroid ADB guide](https://docs.waydro.id/faq/using-adb-with-waydroid)

| What you want                                     | Command                                                      |
| ------------------------------------------------- | ------------------------------------------------------------ |
| Check whether Android finished booting            | `"$ADB" -s "$SERIAL" shell getprop sys.boot_completed`       |
| Check the CPU architecture before choosing an APK | `"$ADB" -s "$SERIAL" shell getprop ro.product.cpu.abi`       |
| Inspect current logical display size and rotation | `"$ADB" -s "$SERIAL" shell dumpsys window displays`          |
| Save a screenshot on Ubuntu                       | `"$ADB" -s "$SERIAL" exec-out screencap -p > screenshot.png` |
| Watch Android logs for this device                | `"$ADB" -s "$SERIAL" logcat`                                 |
| Copy a file from Android to Ubuntu                | `"$ADB" -s "$SERIAL" pull /sdcard/Download/example.txt .`    |
| Disconnect this device from ADB                   | `"$ADB" disconnect "$SERIAL"`                                |

For selector testing, dump the current screen and copy the XML to Ubuntu. Search for `resource-id` to find the exact selector and `bounds` to see its position. [Android ADB reference](https://developer.android.com/tools/adb)

```bash
"$ADB" -s "$SERIAL" shell uiautomator dump /sdcard/window.xml
"$ADB" -s "$SERIAL" pull /sdcard/window.xml .
```
