<script setup lang="ts">
import { computed } from 'vue'
import { AlertCircle } from '@lucide/vue'
import { ApiError, errorMessage } from '../lib/api'

const props = defineProps<{ error: unknown }>()
const message = computed(() => errorMessage(props.error))
const requestID = computed(() => props.error instanceof ApiError ? props.error.requestID : undefined)
</script>

<template>
  <div v-if="error" class="error-banner" role="alert">
    <AlertCircle :size="18" />
    <div><strong>{{ message }}</strong><small v-if="requestID">请求编号：{{ requestID }}</small></div>
  </div>
</template>
