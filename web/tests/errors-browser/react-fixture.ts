import { Component, createElement, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { createClient, createReactErrorHandler } from '../../packages/errors/src/index'
import { browserRuntime } from '../../packages/errors/src/browser'

export async function reactFixture() {
  const records: unknown[] = []
  const client = createClient({
    runtime: browserRuntime,
    loggerProvider: {
      getLogger: () => ({
        enabled: () => true,
        emit: (record) => {
          records.push(record)
        },
      }),
    },
  })
  const report = createReactErrorHandler(client)
  const element = document.createElement('div')
  document.body.append(element)
  class Boundary extends Component<{ children: ReactNode }, { failed: boolean }> {
    state = { failed: false }
    static getDerivedStateFromError() {
      return { failed: true }
    }
    render() {
      return this.state.failed ? 'Fallback visible' : this.props.children
    }
  }
  function Broken(): ReactNode {
    throw new Error('Real React render failure')
  }
  let finish = () => {}
  const caught = new Promise<void>((resolve) => {
    finish = resolve
  })
  const root = createRoot(element, {
    onCaughtError: (error, info) => {
      report(error, info)
      finish()
    },
  })
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    root.render(createElement(Boundary, { children: createElement(Broken) }))
    await Promise.race([
      caught,
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('React callback timeout')), 5000)
      }),
    ])
    return { records, fallback: element.textContent }
  } finally {
    clearTimeout(timer)
    root.unmount()
    element.remove()
    client.dispose()
  }
}
