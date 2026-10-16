/* global document */
document.addEventListener('DOMContentLoaded', () => {
  const names = ['vm-demo: nocx', '/workspace/demo', 'demo-host']
  const context = document.getElementById('model-context-name')
  const keyboard = document.getElementById('model-keyboard-name')
  document.querySelectorAll('[data-demo-tab]').forEach((tab) =>
    tab.addEventListener('click', () => {
      document
        .querySelectorAll('[data-demo-tab]')
        .forEach((item) => item.classList.toggle('selected', item === tab))
      const name = names[Number(tab.dataset.demoTab)]
      if (context) context.textContent = `${name}  ~`
      if (keyboard) keyboard.textContent = name
    }),
  )
  const filter = document.getElementById('demo-filter')
  const rows = [...document.querySelectorAll('.file-tree-row')]
  const empty = document.getElementById('no-files')
  filter?.addEventListener('input', () => {
    const query = filter.value.trim().toLocaleLowerCase()
    let visible = 0
    rows.forEach((row) => {
      row.hidden = !row.textContent.toLocaleLowerCase().includes(query)
      if (!row.hidden) visible += 1
    })
    if (empty) empty.hidden = visible !== 0
  })
})
