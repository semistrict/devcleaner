#!/bin/sh
# Run interactively: notarytool stores credentials in the user's local Keychain.
set -eu
profile=${DEVCLEANER_NOTARY_PROFILE:-devcleaner}
printf 'One-time Apple verification setup. Credentials stay in your Mac keychain.\n'
printf 'Use your Apple Developer account and an app-specific password from https://account.apple.com/\n'
xcrun notarytool store-credentials "$profile"
