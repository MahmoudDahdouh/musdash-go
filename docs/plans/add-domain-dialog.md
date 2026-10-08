# The Add domain dialog

Asked for on 2026-10-08: the dialog an app's domain is added with gets the scheme as a menu in front of the domain, then a port, then the path over the whole width; the two boxes for the www form and the path's removal stay; a button makes a random address; and the user name and password, which "do not work now", are looked at. Three pieces, each planned, the plan reviewed, built and the code reviewed, in that order.

## 1. A password in front of a domain "does not work"

**What was found.** The form, the hash, the routes file and the proxy's check are all right: `test/deploy_test.go` asks a real proxy for a guarded path and gets it. On the machine it was tried on, the domain with the password answered 404, "Nothing is deployed on this address", and never asked for anything. The proxy process there was started from a binary built before paths and passwords existed. Such routes are written under `routes_v2`, a key an earlier proxy does not read, on purpose: it would otherwise serve the host open. So the host was unknown to it. The README says so in one line; the dashboard said nothing, and "Domain added." was the last word a person got.

| # | Decision | Why |
|---|---|---|
| 1 | The proxy says what it reads: on start it writes `<data>/proxy/proxy.format`, `<pid> <format>`, and removes it when it ends. `proxy.RoutesFormat` is 2 | The control plane cannot ask a process what it understands, and a server's proxy may be reached only through a Runner. A file of its own, not a second line of `proxy.pid`: an earlier control plane reads that file as one number |
| 2 | The pid is in the file, and it counts only when it is the pid of `proxy.pid` | A proxy that was killed leaves the file behind; an earlier binary started after it would be taken for a new one |
| 3 | `deploy.ProxyState(ctx, r)` answers running or not, and the format (1 when there is no file or it is another process's). The Servers page uses it in place of its own `proxyRunning` | One reading of the pid file, not two |
| 4 | After a domain with a path or a password is added and the running proxy reads less than format 2, the flash is a warning that says so and what to do: restart the proxy on this machine, install it again on another server | This is the moment the person is looking, and the only place the cause can be named |
| 5 | The Servers page says "Running, an earlier version" with the same advice whenever the format is behind | It is true whether or not a route needs it yet, and it is the page the advice sends a person to |
| 6 | Nothing is refused: the domain is stored and written as before | A proxy that is restarted serves it at once. Refusing would make the order of an upgrade matter |

**Not done:** the running proxy on the machine this was found on is the person's own process and is not restarted from here.

**Tests:** the proxy writes `<pid> 2` and removes it when it ends; a password or a path on a proxy with a pid and no such file comes with the warning and the command, a plain domain does not; another process's file is not the running proxy's; a proxy of this version, and no proxy at all, come with none; the Servers page names an earlier proxy.

**Review of the code.** A proxy built from this tree was run on a spare port with a guarded route: no credentials 401, a wrong password 401, another user name 401, the right pair 200. So nothing but the earlier process stood between the password and the app. The Servers page read the pid file in a function of its own; it is gone, and `ProxyState` is the one reading. The README's line on this now says that the dashboard tells.

## 2. A domain names the port it reaches

**What was there:** an app has one port (`apps.port`), published on one loopback port, and every domain of the app leads to it.

| # | Decision | Why |
|---|---|---|
| 1 | `domains.port`, 0 for "the app's own port". The form's port is stored as 0 when it is the app's | An app whose port is changed in Settings keeps its domains. Every domain there is today is 0 |
| 2 | `app_ports (app_id, port, host_port)`: the loopback port of the serving container for each port a domain of the app names. Replaced in the transaction that records the serving container (`SetAppRuntimePorts`; `SetAppRuntime` is that with none) | A container publishes what it was started with. The routes must name what the serving container has, not what the next one will have, and must change with it in one step |
| 3 | A deployment publishes the app's port and every other port its domains name, each on a free loopback port of its own. A port that is the app's own gets the app's loopback port in `app_ports` and no second publication | One container port cannot be asked for twice by name without two listeners on the host |
| 4 | `RoutesForServer` gives a domain with a port the `app_ports` row's loopback port, and leaves the domain out while there is none | Added while the app runs, the port is not published yet: no route is better than one to the wrong port |
| 5 | Adding such a domain to an app that is serving says "Redeploy to serve it" in the flash | What the dashboard wrote is not live until then |
| 6 | Stopping the app drops its `app_ports` with its loopback port; `UsedHostPorts` counts them | The same life as `apps.host_port` |
| 7 | The health check stays on the app's own port | It is the app's, not a domain's |
| 8 | Only an app's domains have a port. A service's endpoint already is a port | Nothing was asked of services |
| 9 | The domain's row in the tab has a "Port 9000" badge when it is not the app's | Stored state is shown |

**Tests:** a domain with a port added to a running app is in no route until the app is deployed again; the deployment publishes the app's port and the other one once, however many domains name it; each domain's route names its own loopback port, and one that names the app's port by its number the app's; a switch that fails puts the earlier container's ports back; a stopped app holds none. The form stores the app's own port as none, another as itself, refuses what is not a port, and says to redeploy.

**Review of the code.** Read the diff. Docker does not say which of several ports was taken, so a try that fails picks all of them anew; with one port the log line is the one it was. A domain that is removed leaves its row in `app_ports`: the container still publishes the port, so the loopback port is still in use, and the next deployment writes the table anew.

A second reader, given the commit and nothing else, found no way for a route to name a removed container, for two apps to end up on one loopback port, or for a port to reach `docker` unchecked, and two smaller things, both taken. When publishing the routes fails after the file was written (the signal is what failed), the record went back to the earlier container but the file stayed, naming a container about to be removed: the routes are now published once more after the record is put back. And a domain with a new port added while a first deployment was under way got "Domain added." and nothing else, because the app has no serving container yet: the note to redeploy is shown then as well.

## 3. The dialog

```
Scheme       Domain                                   Port
[https:// ⌄] [app.example.com                       ] [3000]
[⟳ Generate an address]  No DNS needed: it leads to the server's IP address, over plain HTTP.

Path
[/api                                                      ]
Optional. …

[ ] Also redirect the www form of the name to it …
[ ] Remove the path before passing a request on …

Ask for a password
User name                      Password
```

| # | Decision | Why |
|---|---|---|
| 1 | The scheme is a `ui.Select` named `scheme` (`https`, `http`), https first. The "Serve over HTTPS" box goes. A form without the field means https; another value is refused | Asked for. One control for one fact |
| 2 | A generated address (`.sslip.io`, `.nip.io`) with https is refused under the Domain field, as a service's endpoint already does | It was silently stored as HTTP. With the scheme in view, what is shown is what is stored |
| 3 | Scheme, Domain and Port are three fields with labels on one line from `sm` up (`.address-row`: the scheme and the port as wide as they need, the domain the rest); below that they stack | Every field keeps a visible label and its error under it (ui-ux-pro-max: forms). The line reads as the address it makes |
| 4 | Port is a text field with `inputmode="numeric"`, filled with the app's own port | 3000 was a guess at the default; the app's port is the one that works without another deployment |
| 5 | "Generate an address" is a button under the line with `data-generate="<input id>"`, `data-suffix=".<ip>.sslip.io"` and `data-scheme="scheme"`: `app.js` puts eight random characters of `[a-z2-7]`, the first a letter, in front of the suffix, and sets the scheme's Select to http | A new one at each press, no request, no inline script. The shape is `secret.RandomID`'s, as the address a new app is offered |
| 6 | The suffix is `deploy.GeneratedSuffix(server)`, which `GeneratedDomain` is built from | One place knows how an IP becomes a name |
| 7 | Path is one field over the whole width, labelled "Path" | Asked for |
| 8 | A Select can be set from script: `app.js` gets `choose(box, value)`, which the click on an option uses too | The generated address is HTTP, and the menu must say so |

**Review of the plan.** Three things changed. Decision 1.5 had the note only while a route needed it; it is shown whenever the proxy is behind, which is simpler and true. A domain's port equal to the app's port at the time of a later deployment (the app's port was changed to it) would have had no row in `app_ports` and so no route: 2.3 gives it the app's loopback port. And the restore after a failed switch (`pipeline.go`) puts back the earlier container: it has to put back that container's ports as well, so they are read before the switch.

**Tests (piece 3):** the dialog has the scheme with https chosen, the domain, the app's port, the path, the two boxes and the password, in that order, the button with the server's suffix, and no HTTPS box; a generated address with https is refused with the reason and comes back with the scheme that was chosen, with http it is stored; a scheme that is neither is refused; a port that is not one is refused with the reason under its field.

**Review of the code (piece 3).** Looked at in a browser at 1024 and at 375 wide. The scheme's button was as wide as its text, so the domain field moved by a few pixels when the other scheme was chosen: the column has one width now and the button fills it. The line under the fields ran to two lines and said the port twice; it is one sentence a fact. Generate gave a new name at each press and the menu showed http ticked; the menu opens above the dialog. A domain made with it, port 9000 and a password, is a row with "HTTP", "Password: ada" and "Port 9000". At 375 the three fields stack and nothing scrolls sideways. The ui-ux-pro-max checks that apply held: every field has a visible label, an error is under its own field, the button has a text label beside its icon, nothing depends on hover.

## 4. The address line, after it was seen

**Asked** on 2026-10-08, with the dialog in front of the person: scheme, domain and port on one line as a flex row, the domain taking the rest; no "://" in the scheme's menu; the scheme and the port as wide as what they hold; the port's field at least four characters wide.

| # | Decision | Why |
|---|---|---|
| 1 | `.address-row` is a flex row at every width, not a grid that stacks under 40rem. The domain is `flex: 1` with `min-width: 0`; the other two do not grow or shrink | Asked for. At 375 wide the domain still has room for about fifteen characters and scrolls its text like any field |
| 2 | The menu's options read `https` and `http` | Asked for. The values the form sends were those already |
| 3 | The scheme's button is as wide as its label. The domain's field moves by one letter's width when the other scheme is chosen | "Fit content" was asked for; the fixed column of piece 3 was there to stop that shift, and is the lesser thing |
| 4 | The port's field is sized by its text where the browser can (`field-sizing: content`), with four digits as the least; elsewhere it is five digits wide, the most a port has. It takes five characters and no more | A width that fits 80, 3000 and 65535 alike without script |
| 5 | An error under the scheme or the port wraps in the field's own width (`width: 0; min-width: 100%`), and the port's error in this dialog is "Enter 1 to 65535." | The sentence would otherwise be the widest thing in the row and push the domain aside. Under a field named Port, the short one says it |

**Review of the plan.** Nothing changed.

**Tests:** the dialog writes no "://"; a port that is not one is refused with the short reason, six digits among them.

**Review of the code.** Measured in a browser: at 1024 the scheme is 81px, the port 57px with four digits or fewer and 65px with five, the domain the remaining 444px, all three on one line; at 375 they are still one line and nothing scrolls sideways.

## 5. The button that makes a domain

**Asked** on 2026-10-08: the button reads "Generate domain" and is at the end.

| # | Decision | Why |
|---|---|---|
| 1 | The label is "Generate domain", and the line beside it says "a generated domain" | Asked for. The field it fills is called Domain |
| 2 | "At the end" is taken as the end of the line under the three fields: the sentence first, the button after it at the line's end (`ms-auto`), under the port. Where the two do not fit on one line the button is on the next, still at the end | In the line of the fields themselves it would take a third of the domain's width at 1024 and leave it none at 375 |

**Review of the plan.** Nothing changed.

**Tests:** the dialog has "Generate domain" after the sentence about a generated domain.

**Review of the code.** In a browser at 1024 the button's right edge is the row's (815px both), and a press fills the domain and sets the scheme to http as before; at 375 the sentence takes the line and the button is under it at the end.

## 6. Three more things about the line under the fields

**Asked** on 2026-10-08: the port's field five characters wide; a new line between "HTTP." and "The port"; the sentence and the button aligned at the start (`align-items: flex-start`).

| # | Decision | Why |
|---|---|---|
| 1 | The port's field is five digits wide whatever it holds; sizing by its text (4.4) goes | Asked for. Five is the most a port has, so nothing is ever cut and the line does not change width as one types |
| 2 | The two sentences are a line each | Asked for. They are about two different fields |
| 3 | The row is `items-start`: the button's top is level with the first sentence | Asked for |

**Review of the plan.** Nothing changed.

**Review of the code.** Measured in a browser at 1024: the port's field is 65px with four digits and with five, and five fit without scrolling; the two sentences are a line each; the row's `align-items` is `flex-start`, and the button's top is the first sentence's.

## 7. The scheme's button keeps one width

**Asked** on 2026-10-08: the scheme's selector is as wide as "https", and only that.

| # | Decision | Why |
|---|---|---|
| 1 | The button's label reserves the width of "https" (the longer of the two, written unseen after the label in the stylesheet), so the button is that wide with either scheme | Asked for. It is the content's own width in the page's font, not a number of pixels, and it takes back what 4.3 gave up: the domain no longer moves when the other scheme is chosen |

**Review of the plan.** Nothing changed.

**Review of the code.** Measured in a browser at 1024: the button is 80.6px wide and 32px high with "https" and with "http", and the domain field's left edge is at the same place with either.
