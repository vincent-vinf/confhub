import * as Primitive from '@radix-ui/react-select'
import { Check, ChevronDown, ChevronUp } from 'lucide-react'
import {
  Children,
  isValidElement,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type OptionHTMLAttributes,
  type ReactNode,
} from 'react'

type SelectProps = Pick<
  ButtonHTMLAttributes<HTMLButtonElement>,
  'id' | 'disabled' | 'aria-label' | 'aria-labelledby' | 'aria-describedby' | 'aria-invalid'
> & {
  value: string | number
  onValueChange: (value: string) => void
  children: ReactNode
}

// Keep option declarations next to their domain data; Radix owns menu behavior,
// keyboard navigation, typeahead, positioning and focus restoration.
export function Select({ value, onValueChange, children, disabled, ...props }: SelectProps) {
  const restoreBackground = useRef<() => void>(() => {})
  const [content, setContent] = useState<HTMLDivElement | null>(null)
  // Radix traps focus and hides the background from screen readers. Inert also
  // removes those hidden controls from the browser's focusable element tree.
  useLayoutEffect(() => {
    if (!content) return
    const isolated: HTMLElement[] = []
    let branch: HTMLElement = content
    while (branch.parentElement) {
      const parent = branch.parentElement
      for (const sibling of parent.children) {
        if (sibling !== branch && sibling instanceof HTMLElement && !sibling.inert) {
          sibling.inert = true
          isolated.push(sibling)
        }
      }
      if (parent === document.body) break
      branch = parent
    }
    const restore = () => {
      for (const element of isolated) element.inert = false
      isolated.length = 0
    }
    restoreBackground.current = restore
    return restore
  }, [content])
  const options = Children.toArray(children).flatMap((child) => {
    if (!isValidElement<OptionHTMLAttributes<HTMLOptionElement>>(child) || child.type !== 'option')
      return []
    const label = Children.toArray(child.props.children).join('')
    return [{ value: String(child.props.value ?? label), label, disabled: child.props.disabled }]
  })
  // Encode every value, including the empty choice, which Radix reserves for placeholders.
  const encode = (item: string | number) => `value:${item}`
  return (
    <Primitive.Root
      value={encode(value)}
      onValueChange={(next) => onValueChange(next.slice(6))}
      disabled={disabled}
    >
      <Primitive.Trigger {...props} className="select-trigger">
        <Primitive.Value placeholder="请选择" />
        <Primitive.Icon className="select-chevron">
          <ChevronDown size={16} aria-hidden="true" />
        </Primitive.Icon>
      </Primitive.Trigger>
      <Primitive.Portal>
        <Primitive.Content
          ref={setContent}
          onCloseAutoFocus={() => restoreBackground.current()}
          className="select-content"
          position="popper"
          sideOffset={6}
          collisionPadding={16}
        >
          <Primitive.ScrollUpButton className="select-scroll">
            <ChevronUp size={16} aria-hidden="true" />
          </Primitive.ScrollUpButton>
          <Primitive.Viewport className="select-viewport">
            {options.map((option) => (
              <Primitive.Item
                key={option.value}
                value={encode(option.value)}
                disabled={option.disabled}
                textValue={option.label}
                className="select-option"
              >
                <Primitive.ItemText>{option.label}</Primitive.ItemText>
                <Primitive.ItemIndicator className="select-indicator">
                  <Check size={16} aria-hidden="true" />
                </Primitive.ItemIndicator>
              </Primitive.Item>
            ))}
          </Primitive.Viewport>
          <Primitive.ScrollDownButton className="select-scroll">
            <ChevronDown size={16} aria-hidden="true" />
          </Primitive.ScrollDownButton>
        </Primitive.Content>
      </Primitive.Portal>
    </Primitive.Root>
  )
}
