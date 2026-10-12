<script setup lang="ts">
import { computed } from 'vue'
import { ChevronLeft, ChevronRight } from '@lucide/vue'

const props = defineProps<{ page: number; pageSize: number; total: number; loading: boolean }>()
defineEmits<{ page: [value: number]; size: [value: number] }>()
const pages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
</script>

<template>
  <div class="pagination">
    <span>共 <strong>{{ total }}</strong> 条记录</span>
    <div class="pagination-controls">
      <label>每页 <select aria-label="每页记录数" :value="pageSize" :disabled="loading" @change="$emit('size', Number(($event.target as HTMLSelectElement).value))"><option :value="10">10</option><option :value="20">20</option><option :value="50">50</option></select> 条</label>
      <span>{{ page }} / {{ pages }}</span>
      <button class="icon-button" aria-label="上一页" :disabled="page <= 1 || loading" @click="$emit('page', page - 1)"><ChevronLeft :size="17" /></button>
      <button class="icon-button" aria-label="下一页" :disabled="page >= pages || loading" @click="$emit('page', page + 1)"><ChevronRight :size="17" /></button>
    </div>
  </div>
</template>
