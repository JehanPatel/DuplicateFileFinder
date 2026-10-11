# Dupe Finder

## WARNING: Make sure to double check before deleting any files crucial to the system.

A small Windows tool that finds duplicate files by content and moves the extra copies to the Recycle Bin.

It is one `.exe` with no installer and no dependencies. It starts a tiny server that only your own computer can reach, and opens a review page in your browser.

## Build

You need Go 1.21 or newer (free, from https://go.dev/dl/).

Open a terminal in this folder and run one of these:

    go build -ldflags="-s -w" -o dupefinder.exe

This keeps a console window open while the app runs. Use it for your first run, because errors show up there.

    go build -ldflags="-s -w -H=windowsgui" -o dupefinder.exe

This hides the console window. Use it once everything works. Quit with the Quit button in the page. The app also closes itself about five minutes after you close the browser tab.

Build on another OS for Windows (64-bit only):

    GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H=windowsgui" -o dupefinder.exe

In PowerShell:

    $env:GOOS="windows"; $env:GOARCH="amd64"; go build -ldflags="-s -w -H=windowsgui" -o dupefinder.exe

The exe is usually a few MB.

## Use

1. Run `dupefinder.exe`. Your browser opens the page.
2. Choose a folder (type a path, use Browse, or click a shortcut) and click Scan folder.
3. Optional: open "Folders to skip" and list any folder you want left alone, one per line. The list is remembered between runs.
4. Pick the copies to remove. "Select duplicates, keeping the Oldest / Newest / Shortest path" does it for every set at once, and you can change any checkbox by hand.
5. Click Move to the Recycle Bin. You are asked to confirm first.

## How it finds duplicates

1. Lists files and keeps only those that share an exact size with another file.
2. Hashes the first 64 KB of each candidate and drops files that differ.
3. Hashes the whole file (SHA-256) for the survivors.

Files are matched by content, so renamed copies are found. Files with zero bytes are ignored.

## Safety rules built in

- Files go to the Recycle Bin, not straight to oblivion. Windows deletes permanently for drives that have no Recycle Bin (some USB sticks and network drives).
- At least one intact copy of every file always stays. The app checks this on the server, not just in the page.
- A file that changed after the scan is skipped. Scan again to refresh.
- These folders are never scanned: `Windows`, `Program Files`, `Program Files (x86)`, `ProgramData`, `AppData`, `.git`, `node_modules`, the Recycle Bin, and `System Volume Information`. Identical files there are normal, and removing them can break programs and projects.
- Driver and setup folders are never scanned either: any folder named `Drivers`, `Driver`, `DriverStore` or `SWSetup`, plus manufacturer and recovery folders that sit directly in a drive root (`Intel`, `AMD`, `NVIDIA`, `Dell`, `HP`, `Lenovo`, `PerfLogs`, `Recovery`, `Windows.old`, `MSOCache`). Driver packages repeat files on purpose so each can install on its own.
- Anything you list under "Folders to skip" is left out, including all of its subfolders. The list is saved in `settings.json` inside a `dupefinder` folder in your Windows user config folder (`%AppData%`).
- Scanning a whole drive (for example `C:\`) asks for confirmation first. Scanning Downloads, Documents, Pictures and Videos is the safer way to clean up.
- Symbolic links are ignored.
- The local server listens on 127.0.0.1 only. Every request needs a random token that only the page it opened knows, so other websites and programs can't tell it to delete files.

## Known limits

- Windows 64-bit only for the Recycle Bin code. On macOS or Linux the tool builds and runs, but "trash" is a folder inside your config directory, meant only for trying it out.
- Paths longer than 260 characters can't be sent to the Recycle Bin and will show an error.
- Windows SmartScreen may warn about an unsigned exe. That is normal for apps without a code-signing certificate.
- It has not been run end to end yet. See "First run checklist".

## First run checklist

Try it on a test folder first.

1. Make a folder with a few copies of some files, including one renamed copy.
2. Scan it. Check that every copy appears in the right set.
3. Select duplicates and move them. Check they are in the Recycle Bin and that one copy of each file is left.
4. Scan your Downloads folder and review before deleting anything.
