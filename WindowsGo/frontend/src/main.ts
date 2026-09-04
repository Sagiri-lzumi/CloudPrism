// main.ts —— 应用入口。
// 样式引入顺序：theme.css(token) → base.css(基元) → components.css(骨架)；
// initTheme 幂等恢复缓存主题（index.html 内联脚本已在首帧前处理）。
import {createApp} from 'vue'
import App from './App.vue'
import './styles/theme.css'
import './styles/base.css'
import './styles/components.css'
import {initTheme} from './lib/theme'

initTheme()

createApp(App).mount('#app')
