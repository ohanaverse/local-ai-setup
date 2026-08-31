# Issues

Follow-up items tracked from feature work. Not blocking; scheduled separately.

## Dead code in `state.py` (from first-class families)

`StateStore.touch_family` and `StateStore.family_display_name` are now dead
production code — no `src/` caller remains after the first-class-families
change moved display-name handling into `registry.toml` `[[families]]`
entries and the `known_families`/`family_display_name` module helpers.

- `src/modelman/state.py` — `touch_family` (docstring stale: "Used both for
  Add Family and to record a display name" is no longer true).
- `src/modelman/state.py` — `family_display_name` method (returns the slug
  fallback; superseded by the `registry.py` module-level helper).

**Action:** remove both methods and their `tests/test_state.py` tests, or
update the docstrings to mark them legacy read-side fallback.

## Stale `_delete_family` docstring

`src/modelman/screens/families.py` — `_delete_family`'s docstring doesn't
mention the registry `[[families]]` entry removal, which is now its primary
destructive effect. One-line docstring touch.
