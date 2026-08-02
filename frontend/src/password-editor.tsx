import { createEffect, createSignal, type Component } from 'solid-js'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { IconButton } from './ui/icon-button'
import { CopyIcon, EyeIcon, ResetIcon } from './ui/icons'
import { Prompt } from './ui/prompt'
import { TextField } from './ui/text-field'
import { showToast } from './ui/toast'

const LOWER = 'abcdefghijkmnopqrstuvwxyz'
const UPPER = 'ABCDEFGHJKLMNPQRSTUVWXYZ'
const DIGITS = '23456789'
const SYMBOLS = '!@#$%^&*_-+='

interface GeneratorOptions {
  upper: boolean
  lower: boolean
  digits: boolean
  symbols: boolean
}

function randomIndex(length: number): number {
  const value = new Uint32Array(1)
  crypto.getRandomValues(value)
  return value[0] % length
}

export function generatePassword(
  length = 20,
  options: GeneratorOptions = { upper: true, lower: true, digits: true, symbols: true },
): string {
  const sets = [
    options.upper ? UPPER : '',
    options.lower ? LOWER : '',
    options.digits ? DIGITS : '',
    options.symbols ? SYMBOLS : '',
  ].filter(Boolean)
  if (sets.length === 0) throw new Error('Select at least one character set')

  const alphabet = sets.join('')
  const chars = sets.map((set) => set[randomIndex(set.length)])
  while (chars.length < length) chars.push(alphabet[randomIndex(alphabet.length)])
  for (let i = chars.length - 1; i > 0; i -= 1) {
    const j = randomIndex(i + 1)
    ;[chars[i], chars[j]] = [chars[j], chars[i]]
  }
  return chars.join('')
}

export interface PasswordEditorProps {
  open: boolean
  value: string
  prompt: string
  onClose: () => void
  onSave: (value: string) => void
}

export const PasswordEditor: Component<PasswordEditorProps> = (props) => {
  const [draft, setDraft] = createSignal('')
  const [length, setLength] = createSignal(20)
  const [upper, setUpper] = createSignal(true)
  const [lower, setLower] = createSignal(true)
  const [digits, setDigits] = createSignal(true)
  const [symbols, setSymbols] = createSignal(true)
  const [visible, setVisible] = createSignal(false)

  createEffect(() => {
    if (props.open) {
      setDraft(props.value)
      setVisible(false)
    }
  })

  const regenerate = (next?: Partial<GeneratorOptions> & { length?: number }) => {
    try {
      setDraft(
        generatePassword(next?.length ?? length(), {
          upper: next?.upper ?? upper(),
          lower: next?.lower ?? lower(),
          digits: next?.digits ?? digits(),
          symbols: next?.symbols ?? symbols(),
        }),
      )
    } catch (err) {
      showToast({ level: 'warning', message: (err as Error).message })
    }
  }

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(draft())
      showToast({ level: 'success', message: 'Password copied' })
    } catch (err) {
      showToast({ level: 'danger', message: `Could not copy password: ${(err as Error).message}` })
    }
  }

  const changeLength = (value: string) => {
    const next = Math.max(5, Math.min(128, Number.parseInt(value, 10) || 5))
    setLength(next)
    regenerate({ length: next })
  }

  const save = () => {
    if (!draft()) {
      showToast({ level: 'warning', message: 'Enter or generate a password' })
      return
    }
    props.onSave(draft())
    props.onClose()
  }

  return (
    <Prompt
      open={props.open}
      onClose={props.onClose}
      // The title says WHICH password (the connection's, not the vault's —
      // nocx-s8jn) and names it; the placeholder repeats it so the eye lands
      // on the same answer wherever it lands.
      ariaLabel={props.prompt}
      placement="top-sheet"
      title={props.prompt}
      actions={
        <>
          <Button variant="primary" onClick={save}>
            OK
          </Button>
          <Button variant="default" onClick={props.onClose}>
            Cancel
          </Button>
        </>
      }
    >
      <div class="password-generator-output">
        <TextField
          id="password-value"
          type={visible() ? 'text' : 'password'}
          value={draft()}
          onInput={setDraft}
          placeholder={props.prompt}
          autoFocus
          trailing={
            <>
              <IconButton
                size="sm"
                ariaLabel={visible() ? 'Hide password' : 'Show password'}
                title={visible() ? 'Hide password' : 'Show password'}
                onClick={() => setVisible((value) => !value)}
              >
                <EyeIcon />
              </IconButton>
              <IconButton
                size="sm"
                ariaLabel="Generate password"
                title="Generate password"
                onClick={() => regenerate()}
              >
                <ResetIcon />
              </IconButton>
              <IconButton
                size="sm"
                ariaLabel="Copy password"
                title="Copy password"
                disabled={!draft()}
                onClick={() => void copy()}
              >
                <CopyIcon />
              </IconButton>
            </>
          }
        />
      </div>
      <div class="password-generator-settings">
        <TextField
          id="password-length"
          type="number"
          label="Length"
          value={length()}
          min={5}
          max={128}
          onInput={changeLength}
        />
        <div class="password-generator-options">
          <Checkbox
            checked={upper()}
            onChange={(checked) => {
              setUpper(checked)
              regenerate({ upper: checked })
            }}
            label="A–Z"
          />
          <Checkbox
            checked={lower()}
            onChange={(checked) => {
              setLower(checked)
              regenerate({ lower: checked })
            }}
            label="a–z"
          />
          <Checkbox
            checked={digits()}
            onChange={(checked) => {
              setDigits(checked)
              regenerate({ digits: checked })
            }}
            label="0–9"
          />
          <Checkbox
            checked={symbols()}
            onChange={(checked) => {
              setSymbols(checked)
              regenerate({ symbols: checked })
            }}
            label="!@#$%^&*"
          />
        </div>
      </div>
    </Prompt>
  )
}
