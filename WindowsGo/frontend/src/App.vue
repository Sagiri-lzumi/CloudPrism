<script setup lang="ts">
// 骨架阶段的占位页。
//
// 唯一职责是打通并可视化 Wails 前后端绑定往返 —— 这正是阶段 2 spike
// 中因 IDE 沙箱禁止 Chromium 建立 Mojo IPC 通道而未能验证的 S1b 项。
// 阶段 6 会被 Fluent 三栏布局整体替换，故此处不写任何业务样式与状态。
import {onMounted, ref} from 'vue'
import {Ping, Quit, Version} from '../wailsjs/go/main/App'
// 临时挂载 Icon 验证 fluent-icons glob 打包（阶段 6 重写整页时移除）
import Icon from './components/fluent/Icon.vue'

const runtimeInfo = ref('读取中…')
const echoResult = ref('尚未调用')
const failure = ref('')

onMounted(async () => {
  try {
    runtimeInfo.value = await Version()
    echoResult.value = await Ping('skeleton')
  } catch (e) {
    failure.value = String(e)
  }
})

async function quit() {
  try {
    await Quit()
  } catch {
    // 进程正在退出，忽略随之而来的 IPC 断开错误
  }
}
</script>

<template>
  <main class="skeleton">
    <h1>CloudPrism <span class="tag">Go</span></h1>
    <p class="hint">工程骨架已就绪，界面将在阶段 6 落地。</p>

    <dl>
      <div>
        <dt>运行时</dt>
        <dd>{{ runtimeInfo }}</dd>
      </div>
      <div>
        <dt>绑定往返</dt>
        <dd :class="{ok: echoResult === 'pong:skeleton'}">{{ echoResult }}</dd>
      </div>
      <div v-if="failure">
        <dt>错误</dt>
        <dd class="bad">{{ failure }}</dd>
      </div>
    </dl>

    <button type="button" @click="quit">退出</button>

    <p class="icons">
      <Icon name="folder" :size="20" />
      <Icon name="document" :size="20" />
      <Icon name="play" :size="20" />
      <Icon name="setting" :size="20" />
    </p>
  </main>
</template>

<style scoped>
.skeleton {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  height: 100%;
  padding: 24px;
  box-sizing: border-box;
}

h1 {
  margin: 0;
  font-size: 24px;
  font-weight: 600;
}

.tag {
  color: var(--accent);
}

.hint {
  margin: 0;
  color: #5c5c5c;
}

dl {
  display: grid;
  gap: 6px;
  margin: 8px 0;
  min-width: 420px;
}

dl > div {
  display: grid;
  grid-template-columns: 96px 1fr;
  gap: 8px;
}

dt {
  color: #5c5c5c;
}

dd {
  margin: 0;
  font-family: Consolas, "Courier New", monospace;
  word-break: break-all;
}

dd.ok {
  color: #2e7d32;
}

dd.bad {
  color: #c62828;
}

button {
  padding: 6px 20px;
  font-family: inherit;
  font-size: inherit;
  color: #fff;
  background: var(--accent);
  border: 1px solid var(--accent);
  border-radius: 4px;
  cursor: pointer;
}

.icons {
  display: flex;
  gap: 12px;
  color: var(--text2);
}
</style>
